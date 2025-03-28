package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	download "websiteCopier/downloader"
	log "websiteCopier/logger"
	"websiteCopier/metrics"
	persist "websiteCopier/persistence"
	read "websiteCopier/reader"
)

const (
	MAX_ROUTINES = 50
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("filePath not provided as argument")
		return
	}

	filePath := os.Args[1]
	urlChan := make(chan string, MAX_ROUTINES) // buffered channel to prevent blocks
	resultChan := make(chan []byte, MAX_ROUTINES)
	semaphore := make(chan struct{}, MAX_ROUTINES) // semaphore to limit max routines to 50
	var mainWg sync.WaitGroup

	// Context to be used to cancel/exit from goroutines on Ctrl+C interrupt
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reader := &read.CSVReader{}
	persister := &persist.TextFileSaver{}
	metrics := &metrics.Metrics{}
	downloader := &download.HTTPDownloader{Metrics: metrics}

	// Stage 1 - Read File.
	mainWg.Add(1)
	go reader.StreamURLs(ctx, filePath, urlChan, &mainWg)

	// Stage 2- Download contents with at max 50 goroutines.
	// Spawning an additional separate goroutine for reading from urlChan
	var wg sync.WaitGroup
	mainWg.Add(1)
	go func(mainWg *sync.WaitGroup) {
		defer mainWg.Done()
		for url := range urlChan {
			semaphore <- struct{}{}
			wg.Add(1)
			go func(ctx context.Context, url string) {
				defer wg.Done()
				downloader.Download(ctx, url, resultChan)
				<-semaphore
			}(ctx, url)
		}
		wg.Wait() // wait till all downloads complete
		close(resultChan)
	}(&mainWg)

	mainWg.Add(1)
	// Stage 3 - Persist contents.
	go persister.SaveToFile(ctx, resultChan, &mainWg)

	// Capture OS signals (Ctrl+C)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan // Wait for Ctrl+C
		log.Warn("Interrupt signal received. Gracefully shutting down after 5 seconds...")

		// Wait for 5 seconds
		<-time.After(5 * time.Second)
		cancel() // Stop every goroutine
	}()

	mainWg.Wait()
	metrics.LogMetrics()
}
