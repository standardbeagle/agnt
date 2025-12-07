package main

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func main() {
	// Make a request that will fail due to no target server
	client := &http.Client{Timeout: 2 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost:9999/test", nil)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		fmt.Printf("Is 'context canceled'? %v\n", err == context.Canceled)
		return
	}
	defer resp.Body.Close()
	fmt.Printf("Status: %d\n", resp.StatusCode)
}
