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
	"sync"
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

// Server holds the torrent server state
type Server struct {
	client      *bittorrent.Client
	downloadDir string
	mu          sync.RWMutex // Protects torrent operations if needed
}

// waitForInfo waits for torrent info with timeout and proper context handling
func (s *Server) waitForInfo(ctx context.Context, torrent *bittorrent.Torrent, timeout time.Duration) error {
	// Check if info is already available
	select {
	case <-torrent.GotInfo():
		return nil
	default:
	}

	// Wait with timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-torrent.GotInfo():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// safeTorrentToResponse safely converts a torrent to response, handling cases where info might not be available
func (s *Server) safeTorrentToResponse(ctx context.Context, torrent *bittorrent.Torrent) (responses.TorrentResponse, error) {
	// Wait for info with a reasonable timeout
	if err := s.waitForInfo(ctx, torrent, 5*time.Second); err != nil {
		// If info is not available, return partial response
		return responses.TorrentResponse{
			InfoHash: torrent.InfoHash().String(),
			Name:     "Loading...",
			Progress: 0,
			Size:     0,
			Files:    []responses.TorrentFile{},
		}, nil
	}

	// Info is available, safe to call methods
	return responses.TorrentToResponse(torrent), nil
}

// contextAwareReader wraps an io.Reader to respect context cancellation
// This is more efficient than creating goroutines per read
type contextAwareReader struct {
	reader io.Reader
	ctx    context.Context
}

func (r *contextAwareReader) Read(p []byte) (n int, err error) {
	// Check if context is cancelled before reading
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
	}

	// Perform the read
	// Note: The underlying reader might still block, but we check context before each read
	// For truly non-blocking behavior, we'd need to use the torrent library's async methods
	n, err = r.reader.Read(p)

	// Check context again after read (in case it was cancelled during read)
	select {
	case <-r.ctx.Done():
		// Context was cancelled, return error
		return n, r.ctx.Err()
	default:
		return n, err
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
	}
	if downloadDir == "" {
		log.Fatal("Download directory flag is required")
	}

	// Set up logging
	if logFile != "" {
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("Failed to open log file %s: %v", logFile, err)
		}
		defer file.Close()
		multiWriter := io.MultiWriter(os.Stderr, file)
		log.SetOutput(multiWriter)
		log.Printf("[STARTUP] Logging to file: %s", logFile)
	} else {
		log.SetOutput(os.Stderr)
	}

	log.Printf("[STARTUP] Starting server on port %d. Downloading to directory: %s", port, downloadDir)

	// Create torrent client
	cfg := bittorrent.NewDefaultClientConfig()
	cfg.DataDir = downloadDir
	cfg.Seed = true

	client, err := bittorrent.NewClient(cfg)
	if err != nil {
		log.Fatalf("[STARTUP] Failed to create bittorrent client: %v", err)
	}
	defer client.Close()

	server := &Server{
		client:      client,
		downloadDir: downloadDir,
	}

	log.Printf("[STARTUP] Bittorrent client created successfully")

	// Set up HTTP router
	r := gin.Default()

	// Health check endpoint
	r.GET("/health-check", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// GET /torrents - List all torrents
	r.GET("/torrents", func(c *gin.Context) {
		ctx := c.Request.Context()
		log.Printf("[GET /torrents] Request received")

		torrents := server.client.Torrents()
		log.Printf("[GET /torrents] Found %d torrents", len(torrents))

		// Convert torrents to responses with timeout protection
		responseChan := make(chan []responses.TorrentResponse, 1)
		errChan := make(chan error, 1)

		go func() {
			responses := make([]responses.TorrentResponse, 0, len(torrents))
			for _, torrent := range torrents {
				resp, err := server.safeTorrentToResponse(ctx, torrent)
				if err != nil {
					log.Printf("[GET /torrents] Warning: Failed to convert torrent %s: %v", torrent.InfoHash().String(), err)
					continue
				}
				responses = append(responses, resp)
			}
			responseChan <- responses
		}()

		// Wait for conversion with timeout
		select {
		case response := <-responseChan:
			log.Printf("[GET /torrents] Returning %d torrents", len(response))
			c.JSON(http.StatusOK, response)
		case err := <-errChan:
			log.Printf("[GET /torrents] Error: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		case <-ctx.Done():
			log.Printf("[GET /torrents] Request cancelled or timed out")
			c.JSON(http.StatusRequestTimeout, gin.H{"error": "Request timeout"})
		case <-time.After(10 * time.Second):
			log.Printf("[GET /torrents] Conversion timeout")
			c.JSON(http.StatusRequestTimeout, gin.H{"error": "Torrent list retrieval timed out"})
		}
	})

	// POST /torrents - Add a new torrent
	r.POST("/torrents", func(c *gin.Context) {
		ctx := c.Request.Context()
		log.Printf("[POST /torrents] Request received")

		var req AddTorrentRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			log.Printf("[POST /torrents] Invalid request: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		log.Printf("[POST /torrents] Adding torrent from file: %s", req.Path)

		// Add torrent from file
		torrent, err := server.client.AddTorrentFromFile(req.Path)
		if err != nil {
			log.Printf("[POST /torrents] Failed to add torrent: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		log.Printf("[POST /torrents] Torrent added, infoHash: %s", torrent.InfoHash().String())

		// Wait for torrent info with timeout
		if err := server.waitForInfo(ctx, torrent, 30*time.Second); err != nil {
			log.Printf("[POST /torrents] Failed to get torrent info: %v", err)
			torrent.Drop()
			c.JSON(http.StatusRequestTimeout, gin.H{
				"error": "Torrent info retrieval timed out after 30 seconds",
			})
			return
		}

		log.Printf("[POST /torrents] Torrent info received")

		// Verify data in background (non-blocking)
		go func() {
			log.Printf("[POST /torrents] Starting background verification for %s", torrent.InfoHash().String())
			torrent.VerifyData()
			log.Printf("[POST /torrents] Verification completed for %s", torrent.InfoHash().String())
		}()

		// Convert to response
		response, err := server.safeTorrentToResponse(ctx, torrent)
		if err != nil {
			log.Printf("[POST /torrents] Failed to convert torrent: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		log.Printf("[POST /torrents] Returning torrent response")
		c.JSON(http.StatusOK, response)
	})

	// GET /torrents/:infoHash - Get a specific torrent
	r.GET("/torrents/:infoHash", func(c *gin.Context) {
		ctx := c.Request.Context()
		infoHashStr := c.Param("infoHash")
		log.Printf("[GET /torrents/:infoHash] Request for: %s", infoHashStr)

		infoHash, err := infohash.FromHexString(infoHashStr)
		if err != nil {
			log.Printf("[GET /torrents/:infoHash] Invalid infoHash: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid infoHash format"})
			return
		}

		torrent, ok := server.client.Torrent(infoHash)
		if !ok {
			log.Printf("[GET /torrents/:infoHash] Torrent not found: %s", infoHashStr)
			c.JSON(http.StatusNotFound, gin.H{"error": "Torrent not found"})
			return
		}

		response, err := server.safeTorrentToResponse(ctx, torrent)
		if err != nil {
			log.Printf("[GET /torrents/:infoHash] Failed to convert torrent: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, response)
	})

	// DELETE /torrents/:infoHash - Delete a torrent
	r.DELETE("/torrents/:infoHash", func(c *gin.Context) {
		ctx := c.Request.Context()
		infoHashStr := c.Param("infoHash")
		log.Printf("[DELETE /torrents/:infoHash] Request for: %s", infoHashStr)

		infoHash, err := infohash.FromHexString(infoHashStr)
		if err != nil {
			log.Printf("[DELETE /torrents/:infoHash] Invalid infoHash: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid infoHash format"})
			return
		}

		torrent, ok := server.client.Torrent(infoHash)
		if !ok {
			log.Printf("[DELETE /torrents/:infoHash] Torrent not found: %s", infoHashStr)
			c.JSON(http.StatusNotFound, gin.H{"error": "Torrent not found"})
			return
		}

		// Try to get torrent name (with timeout)
		var torrentName string
		if err := server.waitForInfo(ctx, torrent, 5*time.Second); err == nil {
			torrentName = torrent.Name()
		} else {
			log.Printf("[DELETE /torrents/:infoHash] Warning: Could not get torrent name, using infoHash")
			torrentName = infoHashStr
		}

		// Drop torrent from client
		torrent.Drop()
		log.Printf("[DELETE /torrents/:infoHash] Torrent dropped from client")

		// Remove data files
		fullPath := path.Join(server.downloadDir, torrentName)
		if err := os.RemoveAll(fullPath); err != nil {
			log.Printf("[DELETE /torrents/:infoHash] Warning: Failed to remove data files: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "Torrent removed but failed to delete data files",
				"details": err.Error(),
			})
			return
		}

		log.Printf("[DELETE /torrents/:infoHash] Torrent and data deleted successfully")
		c.JSON(http.StatusOK, gin.H{
			"message":  "Torrent and data deleted successfully",
			"infoHash": infoHashStr,
		})
	})

	// GET/HEAD /torrents/:infoHash/files/*filePath - Stream a file
	r.Match([]string{"GET", "HEAD"}, "/torrents/:infoHash/files/*filePath", func(c *gin.Context) {
		ctx := c.Request.Context()
		infoHashStr := c.Param("infoHash")
		filepath := strings.TrimPrefix(c.Param("filePath"), "/")
		method := c.Request.Method
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Request - infoHash: %s, file: %s", method, infoHashStr, filepath)

		infoHash, err := infohash.FromHexString(infoHashStr)
		if err != nil {
			log.Printf("[%s /torrents/:infoHash/files/*filePath] Invalid infoHash: %v", method, err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid infoHash format"})
			return
		}

		torrent, ok := server.client.Torrent(infoHash)
		if !ok {
			log.Printf("[%s /torrents/:infoHash/files/*filePath] Torrent not found: %s", method, infoHashStr)
			c.JSON(http.StatusNotFound, gin.H{"error": "Torrent not found"})
			return
		}

		// Wait for torrent info
		if err := server.waitForInfo(ctx, torrent, 30*time.Second); err != nil {
			log.Printf("[%s /torrents/:infoHash/files/*filePath] Failed to get torrent info: %v", method, err)
			c.JSON(http.StatusRequestTimeout, gin.H{
				"error": "Torrent info retrieval timed out",
			})
			return
		}

		// Find the target file
		var targetFile *bittorrent.File
		for _, file := range torrent.Files() {
			if file.Path() == filepath {
				targetFile = file
				break
			}
		}

		if targetFile == nil {
			log.Printf("[%s /torrents/:infoHash/files/*filePath] File not found: %s", method, filepath)
			c.JSON(http.StatusNotFound, gin.H{"error": "File not found: " + filepath})
			return
		}

		fileSize := targetFile.Length()

		// Handle HEAD request
		if method == "HEAD" {
			c.Status(http.StatusOK)
			c.Header("Content-Length", strconv.FormatInt(fileSize, 10))
			c.Header("Content-Type", getContentType(filepath))
			c.Header("Accept-Ranges", "bytes")
			return
		}

		// Parse range header
		rangeHeader := c.GetHeader("Range")
		start, end, err := rangeparser.ParseRangeHeader(rangeHeader, fileSize)
		if err != nil {
			log.Printf("[%s /torrents/:infoHash/files/*filePath] Invalid range header: %v", method, err)
			c.Status(http.StatusRequestedRangeNotSatisfiable)
			c.Header("Accept-Ranges", "bytes")
			c.Header("Content-Type", getContentType(filepath))
			c.Header("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
			return
		}

		// Set response headers
		c.Status(http.StatusPartialContent)
		c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		c.Header("Accept-Ranges", "bytes")
		c.Header("Content-Length", strconv.FormatInt(end-start+1, 10))
		c.Header("Content-Type", getContentType(filepath))

		// Create reader and seek to start position
		reader := targetFile.NewReader()
		defer reader.Close()

		// Seek with timeout
		seekDone := make(chan error, 1)
		go func() {
			_, err := reader.Seek(start, io.SeekStart)
			seekDone <- err
		}()

		select {
		case err := <-seekDone:
			if err != nil {
				log.Printf("[%s /torrents/:infoHash/files/*filePath] Seek failed: %v", method, err)
				c.String(http.StatusInternalServerError, "Failed to seek to position: %v", err)
				return
			}
		case <-ctx.Done():
			log.Printf("[%s /torrents/:infoHash/files/*filePath] Request cancelled during seek", method)
			return
		case <-time.After(5 * time.Second):
			log.Printf("[%s /torrents/:infoHash/files/*filePath] Seek timeout", method)
			c.String(http.StatusRequestTimeout, "Seek operation timed out")
			return
		}

		// Create limited reader for the range
		limitedReader := io.LimitReader(reader, end-start+1)

		// Wrap with context-aware reader
		ctxReader := &contextAwareReader{
			reader: limitedReader,
			ctx:    ctx,
		}

		log.Printf("[%s /torrents/:infoHash/files/*filePath] Starting to stream %d bytes", method, end-start+1)
		c.DataFromReader(http.StatusPartialContent, end-start+1, getContentType(filepath), ctxReader, nil)
		log.Printf("[%s /torrents/:infoHash/files/*filePath] Streaming completed", method)
	})

	log.Printf("[STARTUP] Starting HTTP server on port %d", port)
	if err := r.Run(":" + strconv.Itoa(port)); err != nil {
		log.Fatalf("[STARTUP] Server failed to start: %v", err)
	}
}

func getContentType(filepath string) string {
	ext := strings.ToLower(path.Ext(filepath))
	switch ext {
	case ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".flac":
		return "audio/flac"
	case ".ogg":
		return "audio/ogg"
	case ".srt", ".vtt":
		return "text/vtt"
	default:
		return "application/octet-stream"
	}
}
