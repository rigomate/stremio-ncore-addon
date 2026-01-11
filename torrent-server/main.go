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
		n, err := r.Reader.Read(p)
		resultChan <- readResult{n: n, err: err}
	}()
	
	// Wait for either read completion or timeout/cancellation
	select {
	case result := <-resultChan:
		return result.n, result.err
	case <-readCtx.Done():
		// Timeout or cancellation occurred
		return 0, readCtx.Err()
	}
}

func main() {
	var port int
	var downloadDir string

	flag.IntVar(&port, "p", 0, "Port to run the server on")
	flag.StringVar(&downloadDir, "d", "", "Directory to store downloads")

	flag.Parse()

	if port == 0 {
		log.Fatal("Port flag is required")
		return
	}
	if downloadDir == "" {
		log.Fatal("Download directory flag is required")
		return
	}

	fmt.Printf("Starting server on port %d. Downloading to directory: %s\n", port, downloadDir)

	cfg := bittorrent.NewDefaultClientConfig()
	cfg.DataDir = downloadDir // Store all downloads in a specific directory
	cfg.Seed = true

	client, err := bittorrent.NewClient(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	r := gin.Default()

	r.GET("/health-check", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	r.GET("/torrents", func(c *gin.Context) {
		torrents := client.Torrents()
		
		// Convert torrents to responses with timeout protection
		// Use request context with timeout to prevent blocking
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		
		responseChan := make(chan []responses.TorrentResponse, 1)
		go func() {
			responseChan <- responses.TorrentsToResponse(torrents)
		}()
		
		select {
		case response := <-responseChan:
			c.JSON(http.StatusOK, response)
		case <-ctx.Done():
			// Timeout - return partial or empty response
			log.Printf("Warning: Torrent list retrieval timed out")
			c.JSON(http.StatusRequestTimeout, gin.H{
				"error": "Torrent list retrieval timed out",
			})
		}
	})

	r.POST("/torrents", func(c *gin.Context) {
		var json AddTorrentRequest
		if err := c.ShouldBindBodyWithJSON(&json); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		torrent, err := client.AddTorrentFromFile(json.Path)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		
		// Wait for torrent info with timeout to prevent infinite blocking
		// Use request context so client disconnection cancels the operation
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		
		select {
		case <-torrent.GotInfo():
			// Torrent info received successfully
		case <-ctx.Done():
			// Clean up the torrent if we timed out
			torrent.Drop()
			c.JSON(http.StatusRequestTimeout, gin.H{
				"error": "Torrent info retrieval timed out after 30 seconds",
			})
			return
		}
		
		// VerifyData can also block, so run it with timeout
		verifyCtx, verifyCancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer verifyCancel()
		
		verifyDone := make(chan error, 1)
		go func() {
			torrent.VerifyData()
			verifyDone <- nil
		}()
		
		select {
		case <-verifyDone:
			// Verification completed
		case <-verifyCtx.Done():
			// Verification timed out, but continue anyway
			log.Printf("Warning: Torrent verification timed out for %s", json.Path)
		}
		
		response := responses.TorrentToResponse(torrent)
		c.JSON(http.StatusOK, response)
	})

	r.GET("/torrents/:infoHash", func(c *gin.Context) {
		infoHash := c.Param("infoHash")
		torrent, ok := client.Torrent(infohash.FromHexString(infoHash))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "Torrent not found"})
			return
		}
		response := responses.TorrentToResponse(torrent)
		c.JSON(http.StatusOK, response)
	})

	r.DELETE("/torrents/:infoHash", func(c *gin.Context) {
		infoHash := c.Param("infoHash")
		torrent, ok := client.Torrent(infohash.FromHexString(infoHash))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "Torrent not found"})
			return
		}
		
		// Ensure torrent has info before getting name (with timeout)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		
		select {
		case <-torrent.GotInfo():
			// Torrent info available
		case <-ctx.Done():
			// Timeout - try to get name anyway, it might work
			log.Printf("Warning: Torrent info not available when deleting %s", infoHash)
		}
		
		// Get torrent's root directory/files before dropping
		downloadPath := cfg.DataDir
		torrentName := torrent.Name()

		torrent.Drop()

		// Remove the data files
		fullPath := path.Join(downloadPath, torrentName)
		err := os.RemoveAll(fullPath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "Torrent removed but failed to delete data files",
				"details": err.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":  "Torrent and data deleted successfully",
			"infoHash": infoHash,
		})
	})

	r.Match([]string{"GET", "HEAD"}, "/torrents/:infoHash/files/*filePath", func(c *gin.Context) {
		infoHash := c.Param("infoHash")
		filepath := strings.TrimPrefix(c.Param("filePath"), "/")
		torrent, ok := client.Torrent(infohash.FromHexString(infoHash))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "Torrent not found"})
			return
		}

		// Wait for torrent info with timeout to prevent infinite blocking
		// Use request context so client disconnection cancels the operation
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		
		select {
		case <-torrent.GotInfo():
			// Torrent info received successfully
		case <-ctx.Done():
			c.JSON(http.StatusRequestTimeout, gin.H{
				"error": "Torrent info retrieval timed out after 30 seconds",
			})
			return
		}
		
		var targetFile *bittorrent.File
		for _, file := range torrent.Files() {
			if file.Path() == filepath {
				targetFile = file
				break
			}
		}
		if targetFile == nil {
			println(filepath)
			c.JSON(http.StatusNotFound, gin.H{"error": "File not found: " + filepath})
			return
		}

		if c.Request.Method == "HEAD" {
			c.Status(http.StatusOK)
			c.Header("Content-Length", strconv.Itoa(int(targetFile.Length())))
			c.Header("Content-Type", getContentType(targetFile.Path()))
			c.Header("Accept-Ranges", "bytes")
			return
		}

		// Get file size
		fileSize := targetFile.Length()

		// Parse range header
		rangeHeader := c.GetHeader("Range")
		start, end, err := rangeparser.ParseRangeHeader(rangeHeader, fileSize)

		if err != nil {
			fmt.Println(err)
			c.Status(http.StatusRequestedRangeNotSatisfiable)
			c.Header("Accept-Ranges", "bytes")
			c.Header("Content-Type", getContentType(filepath))
			c.Header("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
			return
		}

		// Set headers
		c.Status(http.StatusPartialContent)
		c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		c.Header("Accept-Ranges", "bytes")
		c.Header("Content-Length", fmt.Sprintf("%d", end-start+1))
		c.Header("Content-Type", getContentType(filepath))

		// Create reader for the specific range with context cancellation
		// This ensures the reader can be cancelled if the client disconnects or times out
		reader := targetFile.NewReader()
		defer reader.Close()
		
		// Seek with timeout protection
		seekCtx, seekCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer seekCancel()
		
		seekDone := make(chan error, 1)
		go func() {
			_, err := reader.Seek(start, io.SeekStart)
			seekDone <- err
		}()
		
		select {
		case err := <-seekDone:
			if err != nil {
				c.String(http.StatusInternalServerError, "Failed to seek to position: %v", err)
				return
			}
		case <-seekCtx.Done():
			c.String(http.StatusRequestTimeout, "Seek operation timed out")
			return
		}

		// Stream the range with context-aware reader
		// Create a limited reader to read only the requested range
		limitedReader := io.LimitReader(reader, end-start+1)
		
		// Wrap the reader with context cancellation to prevent infinite blocking
		// Use a shorter timeout per read (10 seconds) - if a single read blocks this long,
		// something is wrong and we should fail rather than wait indefinitely
		ctxReader := &contextReader{
			Reader:  limitedReader,
			ctx:     c.Request.Context(),
			timeout: 10 * time.Second, // Maximum time per read operation
		}
		
		c.DataFromReader(http.StatusPartialContent, end-start+1, getContentType(filepath), ctxReader, nil)
	})

	r.Run(":" + strconv.Itoa(port))
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
