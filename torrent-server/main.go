package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	bittorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/types/infohash"
	rangeparser "github.com/detarkende/stremio-ncore-addon/torrent-server/internal/range-parser"
	responses "github.com/detarkende/stremio-ncore-addon/torrent-server/internal/responses"
	gin "github.com/gin-gonic/gin"
)

type AddTorrentRequest struct {
	Path string `json:"path"`
}

// contextReader wraps an io.Reader with context cancellation and timeout
// to prevent infinite blocking when reading from torrent files
type contextReader struct {
	io.Reader
	ctx     context.Context
	timeout time.Duration
}

func (r *contextReader) Read(p []byte) (n int, err error) {
	log.Printf("[READ] Starting read operation, buffer size: %d, timeout: %v", len(p), r.timeout)
	startTime := time.Now()
	
	// Create a timeout context for this read operation
	readCtx, cancel := context.WithTimeout(r.ctx, r.timeout)
	defer cancel()
	
	// Channel to receive read result
	type readResult struct {
		n   int
		err error
	}
	resultChan := make(chan readResult, 1)
	
	// Perform read in goroutine
	go func() {
		log.Printf("[READ] Goroutine: Starting actual read from underlying reader")
		readStart := time.Now()
		n, err := r.Reader.Read(p)
		readDuration := time.Since(readStart)
		log.Printf("[READ] Goroutine: Read completed, bytes: %d, error: %v, duration: %v", n, err, readDuration)
		resultChan <- readResult{n: n, err: err}
	}()
	
	// Wait for either read completion or timeout/cancellation
	select {
	case result := <-resultChan:
		duration := time.Since(startTime)
		log.Printf("[READ] Read succeeded, bytes: %d, error: %v, total duration: %v", result.n, result.err, duration)
		return result.n, result.err
	case <-readCtx.Done():
		// Timeout or cancellation occurred
		duration := time.Since(startTime)
		log.Printf("[READ] Read timed out or cancelled after %v, error: %v", duration, readCtx.Err())
		return 0, readCtx.Err()
	}
}

func main() {
	var port int
	var downloadDir string
	var logFile string

	flag.IntVar(&port, "p", 0, "Port to run the server on")
	flag.StringVar(&downloadDir, "d", "", "Directory to store downloads")
	flag.StringVar(&logFile, "log", "", "Optional log file path (logs will also go to stderr)")

	flag.Parse()

	if port == 0 {
		log.Fatal("Port flag is required")
		return
	}
	if downloadDir == "" {
		log.Fatal("Download directory flag is required")
		return
	}

	// Set up logging: write to both stderr and optionally to a file
	if logFile != "" {
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("Failed to open log file %s: %v", logFile, err)
		}
		defer file.Close()
		// Create a multi-writer to write to both stderr and file
		multiWriter := io.MultiWriter(os.Stderr, file)
		log.SetOutput(multiWriter)
		log.Printf("[STARTUP] Logging to file: %s", logFile)
	} else {
		// Default: log to stderr only
		log.SetOutput(os.Stderr)
	}

	log.Printf("[STARTUP] Starting server on port %d. Downloading to directory: %s", port, downloadDir)

	cfg := bittorrent.NewDefaultClientConfig()
	cfg.DataDir = downloadDir // Store all downloads in a specific directory
	cfg.Seed = true

	log.Printf("[STARTUP] Creating bittorrent client")
	client, err := bittorrent.NewClient(cfg)
	if err != nil {
		log.Fatalf("[STARTUP] ERROR: Failed to create bittorrent client: %v", err)
	}
	defer client.Close()
	log.Printf("[STARTUP] Bittorrent client created successfully")

	log.Printf("[STARTUP] Setting up HTTP routes")
	r := gin.Default()
	log.Printf("[STARTUP] HTTP routes configured")

	r.GET("/health-check", func(c *gin.Context) {
		log.Printf("[GET /health-check] Health check requested at %v", time.Now())
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		log.Printf("[GET /health-check] Health check completed")
	})

	r.GET("/torrents", func(c *gin.Context) {
		log.Printf("[GET /torrents] Request received at %v", time.Now())
		startTime := time.Now()
		
		log.Printf("[GET /torrents] Getting torrents from client")
		torrents := client.Torrents()
		log.Printf("[GET /torrents] Got %d torrents from client", len(torrents))
		
		// Convert torrents to responses with timeout protection
		// Use request context with timeout to prevent blocking
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		
		log.Printf("[GET /torrents] Starting response conversion in goroutine")
		responseChan := make(chan []responses.TorrentResponse, 1)
		go func() {
			log.Printf("[GET /torrents] Goroutine: Starting TorrentsToResponse conversion")
			convStart := time.Now()
			response := responses.TorrentsToResponse(torrents)
			convDuration := time.Since(convStart)
			log.Printf("[GET /torrents] Goroutine: Conversion completed in %v, %d responses", convDuration, len(response))
			responseChan <- response
		}()
		
		log.Printf("[GET /torrents] Waiting for response or timeout")
		select {
		case response := <-responseChan:
			duration := time.Since(startTime)
			log.Printf("[GET /torrents] Response ready, sending %d torrents, total duration: %v", len(response), duration)
			c.JSON(http.StatusOK, response)
			log.Printf("[GET /torrents] Request completed successfully")
		case <-ctx.Done():
			// Timeout - return partial or empty response
			duration := time.Since(startTime)
			log.Printf("[GET /torrents] ERROR: Torrent list retrieval timed out after %v", duration)
			c.JSON(http.StatusRequestTimeout, gin.H{
				"error": "Torrent list retrieval timed out",
			})
		}
	})

	r.POST("/torrents", func(c *gin.Context) {
		log.Printf("[POST /torrents] Request received at %v", time.Now())
		startTime := time.Now()
		
		var json AddTorrentRequest
		log.Printf("[POST /torrents] Parsing request body")
		if err := c.ShouldBindBodyWithJSON(&json); err != nil {
			log.Printf("[POST /torrents] ERROR: Failed to parse request body: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		log.Printf("[POST /torrents] Request body parsed, path: %s", json.Path)
		
		log.Printf("[POST /torrents] Adding torrent from file: %s", json.Path)
		addStart := time.Now()
		torrent, err := client.AddTorrentFromFile(json.Path)
		addDuration := time.Since(addStart)
		if err != nil {
			log.Printf("[POST /torrents] ERROR: Failed to add torrent: %v, duration: %v", err, addDuration)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		log.Printf("[POST /torrents] Torrent added successfully in %v, infoHash: %s", addDuration, torrent.InfoHash().String())
		
		// Wait for torrent info with timeout to prevent infinite blocking
		// Use request context so client disconnection cancels the operation
		log.Printf("[POST /torrents] Waiting for torrent info (timeout: 30s)")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		
		infoStart := time.Now()
		select {
		case <-torrent.GotInfo():
			infoDuration := time.Since(infoStart)
			log.Printf("[POST /torrents] Torrent info received successfully in %v", infoDuration)
		case <-ctx.Done():
			infoDuration := time.Since(infoStart)
			// Clean up the torrent if we timed out
			log.Printf("[POST /torrents] ERROR: Torrent info retrieval timed out after %v, dropping torrent", infoDuration)
			torrent.Drop()
			c.JSON(http.StatusRequestTimeout, gin.H{
				"error": "Torrent info retrieval timed out after 30 seconds",
			})
			return
		}
		
		// VerifyData can also block, so run it with timeout
		log.Printf("[POST /torrents] Starting data verification (timeout: 10s)")
		verifyCtx, verifyCancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer verifyCancel()
		
		verifyDone := make(chan error, 1)
		verifyStart := time.Now()
		go func() {
			log.Printf("[POST /torrents] Goroutine: Starting VerifyData()")
			torrent.VerifyData()
			verifyDuration := time.Since(verifyStart)
			log.Printf("[POST /torrents] Goroutine: VerifyData() completed in %v", verifyDuration)
			verifyDone <- nil
		}()
		
		select {
		case <-verifyDone:
			verifyDuration := time.Since(verifyStart)
			log.Printf("[POST /torrents] Verification completed in %v", verifyDuration)
		case <-verifyCtx.Done():
			verifyDuration := time.Since(verifyStart)
			// Verification timed out, but continue anyway
			log.Printf("[POST /torrents] WARNING: Torrent verification timed out after %v for %s", verifyDuration, json.Path)
		}
		
		log.Printf("[POST /torrents] Converting torrent to response")
		responseStart := time.Now()
		response := responses.TorrentToResponse(torrent)
		responseDuration := time.Since(responseStart)
		log.Printf("[POST /torrents] Response conversion completed in %v", responseDuration)
		
		totalDuration := time.Since(startTime)
		log.Printf("[POST /torrents] Sending response, total duration: %v", totalDuration)
		c.JSON(http.StatusOK, response)
		log.Printf("[POST /torrents] Request completed successfully")
	})

	r.GET("/torrents/:infoHash", func(c *gin.Context) {
		infoHash := c.Param("infoHash")
		log.Printf("[GET /torrents/:infoHash] Request received for infoHash: %s at %v", infoHash, time.Now())
		
		log.Printf("[GET /torrents/:infoHash] Looking up torrent in client")
		torrent, ok := client.Torrent(infohash.FromHexString(infoHash))
		if !ok {
			log.Printf("[GET /torrents/:infoHash] ERROR: Torrent not found: %s", infoHash)
			c.JSON(http.StatusNotFound, gin.H{"error": "Torrent not found"})
			return
		}
		log.Printf("[GET /torrents/:infoHash] Torrent found, converting to response")
		
		response := responses.TorrentToResponse(torrent)
		log.Printf("[GET /torrents/:infoHash] Sending response")
		c.JSON(http.StatusOK, response)
		log.Printf("[GET /torrents/:infoHash] Request completed")
	})

	r.DELETE("/torrents/:infoHash", func(c *gin.Context) {
		infoHash := c.Param("infoHash")
		log.Printf("[DELETE /torrents/:infoHash] Request received for infoHash: %s at %v", infoHash, time.Now())
		
		log.Printf("[DELETE /torrents/:infoHash] Looking up torrent in client")
		torrent, ok := client.Torrent(infohash.FromHexString(infoHash))
		if !ok {
			log.Printf("[DELETE /torrents/:infoHash] ERROR: Torrent not found: %s", infoHash)
			c.JSON(http.StatusNotFound, gin.H{"error": "Torrent not found"})
			return
		}
		log.Printf("[DELETE /torrents/:infoHash] Torrent found")
		
		// Ensure torrent has info before getting name (with timeout)
		log.Printf("[DELETE /torrents/:infoHash] Waiting for torrent info (timeout: 5s)")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		
		select {
		case <-torrent.GotInfo():
			log.Printf("[DELETE /torrents/:infoHash] Torrent info available")
		case <-ctx.Done():
			// Timeout - try to get name anyway, it might work
			log.Printf("[DELETE /torrents/:infoHash] WARNING: Torrent info not available when deleting %s", infoHash)
		}
		
		// Get torrent's root directory/files before dropping
		downloadPath := cfg.DataDir
		log.Printf("[DELETE /torrents/:infoHash] Getting torrent name")
		torrentName := torrent.Name()
		log.Printf("[DELETE /torrents/:infoHash] Torrent name: %s", torrentName)

		log.Printf("[DELETE /torrents/:infoHash] Dropping torrent from client")
		torrent.Drop()
		log.Printf("[DELETE /torrents/:infoHash] Torrent dropped")

		// Remove the data files
		fullPath := path.Join(downloadPath, torrentName)
		log.Printf("[DELETE /torrents/:infoHash] Removing data files from: %s", fullPath)
		err := os.RemoveAll(fullPath)
		if err != nil {
			log.Printf("[DELETE /torrents/:infoHash] ERROR: Failed to remove data files: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "Torrent removed but failed to delete data files",
				"details": err.Error(),
			})
			return
		}
		log.Printf("[DELETE /torrents/:infoHash] Data files removed successfully")

		c.JSON(http.StatusOK, gin.H{
			"message":  "Torrent and data deleted successfully",
			"infoHash": infoHash,
		})
		log.Printf("[DELETE /torrents/:infoHash] Request completed")
	})

	r.Match([]string{"GET", "HEAD"}, "/torrents/:infoHash/files/*filePath", func(c *gin.Context) {
		infoHash := c.Param("infoHash")
		filepath := strings.TrimPrefix(c.Param("filePath"), "/")
		method := c.Request.Method
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Request received at %v, infoHash: %s, filePath: %s", method, time.Now(), infoHash, filepath)
		startTime := time.Now()
		
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Looking up torrent in client", method)
		torrent, ok := client.Torrent(infohash.FromHexString(infoHash))
		if !ok {
			log.Printf("[%s /torrents/:infoHash/files/*filePath] ERROR: Torrent not found: %s", method, infoHash)
			c.JSON(http.StatusNotFound, gin.H{"error": "Torrent not found"})
			return
		}
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Torrent found", method)

		// Wait for torrent info with timeout to prevent infinite blocking
		// Use request context so client disconnection cancels the operation
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Waiting for torrent info (timeout: 30s)", method)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		
		infoStart := time.Now()
		select {
		case <-torrent.GotInfo():
			infoDuration := time.Since(infoStart)
			log.Printf("[%s /torrents/:infoHash/files/*filePath] Torrent info received in %v", method, infoDuration)
		case <-ctx.Done():
			infoDuration := time.Since(infoStart)
			log.Printf("[%s /torrents/:infoHash/files/*filePath] ERROR: Torrent info retrieval timed out after %v", method, infoDuration)
			c.JSON(http.StatusRequestTimeout, gin.H{
				"error": "Torrent info retrieval timed out after 30 seconds",
			})
			return
		}
		
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Searching for file in torrent", method)
		var targetFile *bittorrent.File
		for _, file := range torrent.Files() {
			if file.Path() == filepath {
				targetFile = file
				break
			}
		}
		if targetFile == nil {
			log.Printf("[%s /torrents/:infoHash/files/*filePath] ERROR: File not found: %s", method, filepath)
			c.JSON(http.StatusNotFound, gin.H{"error": "File not found: " + filepath})
			return
		}
		log.Printf("[%s /torrents/:infoHash/files/*filePath] File found", method)

		if c.Request.Method == "HEAD" {
			log.Printf("[%s /torrents/:infoHash/files/*filePath] Sending HEAD response", method)
			c.Status(http.StatusOK)
			c.Header("Content-Length", strconv.Itoa(int(targetFile.Length())))
			c.Header("Content-Type", getContentType(targetFile.Path()))
			c.Header("Accept-Ranges", "bytes")
			log.Printf("[%s /torrents/:infoHash/files/*filePath] HEAD request completed", method)
			return
		}

		// Get file size
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Getting file size", method)
		fileSize := targetFile.Length()
		log.Printf("[%s /torrents/:infoHash/files/*filePath] File size: %d", method, fileSize)

		// Parse range header
		rangeHeader := c.GetHeader("Range")
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Parsing range header: %s", method, rangeHeader)
		start, end, err := rangeparser.ParseRangeHeader(rangeHeader, fileSize)

		if err != nil {
			log.Printf("[%s /torrents/:infoHash/files/*filePath] ERROR: Failed to parse range header: %v", method, err)
			c.Status(http.StatusRequestedRangeNotSatisfiable)
			c.Header("Accept-Ranges", "bytes")
			c.Header("Content-Type", getContentType(filepath))
			c.Header("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
			return
		}
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Range parsed: %d-%d", method, start, end)

		// Set headers
		c.Status(http.StatusPartialContent)
		c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		c.Header("Accept-Ranges", "bytes")
		c.Header("Content-Length", fmt.Sprintf("%d", end-start+1))
		c.Header("Content-Type", getContentType(filepath))
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Headers set, content-length: %d", method, end-start+1)

		// Create reader for the specific range with context cancellation
		// This ensures the reader can be cancelled if the client disconnects or times out
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Creating file reader", method)
		reader := targetFile.NewReader()
		defer reader.Close()
		log.Printf("[%s /torrents/:infoHash/files/*filePath] File reader created", method)
		
		// Seek with timeout protection
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Seeking to position %d (timeout: 5s)", method, start)
		seekCtx, seekCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer seekCancel()
		
		seekDone := make(chan error, 1)
		seekStart := time.Now()
		go func() {
			log.Printf("[%s /torrents/:infoHash/files/*filePath] Goroutine: Starting seek operation", method)
			_, err := reader.Seek(start, io.SeekStart)
			seekDuration := time.Since(seekStart)
			log.Printf("[%s /torrents/:infoHash/files/*filePath] Goroutine: Seek completed in %v, error: %v", method, seekDuration, err)
			seekDone <- err
		}()
		
		select {
		case err := <-seekDone:
			seekDuration := time.Since(seekStart)
			if err != nil {
				log.Printf("[%s /torrents/:infoHash/files/*filePath] ERROR: Seek failed after %v: %v", method, seekDuration, err)
				c.String(http.StatusInternalServerError, "Failed to seek to position: %v", err)
				return
			}
			log.Printf("[%s /torrents/:infoHash/files/*filePath] Seek completed successfully in %v", method, seekDuration)
		case <-seekCtx.Done():
			seekDuration := time.Since(seekStart)
			log.Printf("[%s /torrents/:infoHash/files/*filePath] ERROR: Seek operation timed out after %v", method, seekDuration)
			c.String(http.StatusRequestTimeout, "Seek operation timed out")
			return
		}

		// Stream the range with context-aware reader
		// Create a limited reader to read only the requested range
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Creating limited reader for %d bytes", method, end-start+1)
		limitedReader := io.LimitReader(reader, end-start+1)
		
		// Wrap the reader with context cancellation to prevent infinite blocking
		// Use a shorter timeout per read (10 seconds) - if a single read blocks this long,
		// something is wrong and we should fail rather than wait indefinitely
		ctxReader := &contextReader{
			Reader:  limitedReader,
			ctx:     c.Request.Context(),
			timeout: 10 * time.Second, // Maximum time per read operation
		}
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Starting to stream data via DataFromReader", method)
		streamStart := time.Now()
		
		c.DataFromReader(http.StatusPartialContent, end-start+1, getContentType(filepath), ctxReader, nil)
		
		streamDuration := time.Since(streamStart)
		totalDuration := time.Since(startTime)
		log.Printf("[%s /torrents/:infoHash/files/*filePath] DataFromReader completed, stream duration: %v, total duration: %v", method, streamDuration, totalDuration)
	})

	log.Printf("[STARTUP] Starting HTTP server on port %d", port)
	log.Printf("[STARTUP] Server is ready to accept connections")
	if err := r.Run(":" + strconv.Itoa(port)); err != nil {
		log.Fatalf("[STARTUP] ERROR: Server failed to start: %v", err)
	}
}

func getContentType(filepath string) string {
	ext := strings.ToLower(path.Ext(filepath))
	switch ext {
	case ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	default:
		return "application/octet-stream"
	}
}
