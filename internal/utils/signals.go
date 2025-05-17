package utils

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

// SetupSignalHandlers sets up signal handlers for graceful shutdown
func SetupSignalHandlers(cleanup func() error) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-c
		log.Printf("Received signal: %v", sig)
		log.Println("Closing connections...")
		
		if err := cleanup(); err != nil {
			log.Printf("Cleanup error: %v", err)
		}
		
		os.Exit(0)
	}()
}
