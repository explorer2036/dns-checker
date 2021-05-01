package main

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// startTimerJob news a timer for doing the function
func startTimerJob(wg *sync.WaitGroup, done chan struct{}, name string, interval time.Duration, beforeTimer bool, fn func() error) error {
	wg.Add(1)

	if beforeTimer {
		// do the function at the start
		if err := fn(); err != nil {
			log.Printf("timer for %s: %v", name, err)
		}
	}

	go func() {
		defer wg.Done()

		// new a timer for doing the function periodly
		ticker := time.NewTimer(interval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				// do the function
				if err := fn(); err != nil {
					log.Printf("timer for %s: %v", name, err)
				}

				// reset the timer
				ticker.Reset(interval)
			}
		}
	}()

	return nil
}

// check if the address is available
func checkAddress(address string) error {
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	return nil
}

// runScript executes the script and returns the output
func runScript(script string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command("bash", "-c", script)
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("command run {%s}: %v", script, err)
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}
