package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	members  = flag.Int("members", 3, "the members of group for checking concurrently")
	wait     = flag.Int64("sleep", 1000, "the sleep time for every dns(unit: ms)")
	interval = flag.Int64("interval", 60*30, "the interval time for syncing the dns list(unit: s)")
	timeout  = flag.Int64("timeout", 5, "the timeout for dialing the dns port(unit: s)")
	// verify   = flag.Int64("verify", 60*30	, "the interval time for authorizing the dns checker(unit: s)")
	serial = flag.String("serial", "", "the serial number for every device")
	debug  = flag.String("debug", "", "the debug can be enabled with a specific code")
)

const (
	StatusUP   = 0
	StatusDown = 1
	Success    = "success"
	DebugCode  = "vaeDWFXyWN9tuR2g"
	Retries    = 3
	CheckTimes = 2

	FetchURL = "http://comki.mypanel.cc/api/rp/f"
	DownURL  = "http://comki.mypanel.cc/api/rp/d"
	UpURL    = "http://comki.mypanel.cc/api/rp/u"
	AuthURL  = "http://comki.mypanel.cc/api/rp/h"
)

var (
	// done for notifying the goroutines to exit
	done = make(chan struct{})
	// tasks for the changes of dns list
	tasks = make(chan []*Status)
	// previous dns list for pinging
	previous []*Status
	// the goroutine counter
	goroutines uint64
	// pointer for managing the goroutines
	pointer chan struct{}
)

// Status for the current status of a dns
type Status struct {
	ID      int    `json:"id"`
	Address string `json:"dns"`
	Status  int    `json:"status"`
}

// Response for the dns list
type Response struct {
	Result string             `json:"result"`
	List   map[string]*Status `json:"list"`
}

func doRequest(url string, body io.Reader, v interface{}) error {
	// new a request for url and body
	request, err := http.NewRequest("POST", url, body)
	if err != nil {
		return fmt.Errorf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// new a http client with timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// send the request and get the response
	resp, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("do request: %v", err)
	}
	defer resp.Body.Close()

	// check the http status code
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status code: %v", resp.StatusCode)
	}

	// read the response data and json unmarshal the string
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ioutil read: %v", err)
	}
	if json.Valid(data) {
		if err := json.Unmarshal(data, v); err != nil {
			return fmt.Errorf("json unmarshal: %v", err)
		}
	} else {
		return fmt.Errorf("invalid json string: %v", string(data))
	}

	return nil
}

// compare the previous and current tasks
func compareTasks(pre, cur []*Status) bool {
	if len(pre) != len(cur) {
		return false
	}
	for i := 0; i < len(pre); i++ {
		if pre[i].Address != cur[i].Address || previous[i].ID != cur[i].ID {
			return false
		}
	}

	return true
}

func handleTasks(wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-done:
			return
		case current := <-tasks:
			if len(previous) > 0 {
				// compare the previous and current tasks
				if compareTasks(previous, current) {
					continue
				}
			}

			if count := len(current); count > 0 {
				// stop the old goroutines
				if pointer != nil {
					close(pointer)
					pointer = nil
				}

				// create a new channel for new goroutines
				pointer = make(chan struct{})

				number := *members
				// start new goroutines for pinging the dns list
				mod, rem := count/number, count%number
				for i := 0; i < mod; i++ {
					wg.Add(1)
					go ping(wg, pointer, current, i*number, (i+1)*number, time.Duration(*wait)*time.Millisecond)

					goroutines++
				}
				if rem > 0 {
					wg.Add(1)
					go ping(wg, pointer, current, mod*number, count, time.Duration(*wait)*time.Millisecond)

					goroutines++
				}
			}

			// update the previous tasks
			previous = current
		}
	}
}

// update the dns status to server
func update(cur *Status, status int) error {
	params := url.Values{}
	params.Add("id", strconv.Itoa(cur.ID))
	params.Add("s", *serial)
	body := strings.NewReader(params.Encode())

	url := DownURL
	if status == StatusUP {
		url = UpURL
	}

	var res Response
	if err := doRequest(url, body, &res); err != nil {
		if status == StatusDown {
			return fmt.Errorf("status down for %v: %v", cur, err)
		} else {
			return fmt.Errorf("status up for %v: %v", cur, err)
		}
	} else {
		if res.Result == Success {
			cur.Status = status

			if *debug == DebugCode {
				if status == StatusDown {
					log.Printf("status down for %v", cur)
				} else {
					log.Printf("status up for %v", cur)
				}
			}
		} else {
			return fmt.Errorf("do request for %s: %v", url, res.Result)
		}
	}

	return nil
}

func ping(wg *sync.WaitGroup, done chan struct{}, tasks []*Status, start int, end int, delay time.Duration) {
	defer wg.Done()

	offset := start
	for {
		select {
		case <-done:
			goroutines--
			return
		default:
		}

		cur := tasks[offset]

		available := false
		// check if the address is available twice
		for i := 0; i < CheckTimes; i++ {
			if err := checkAddress(cur.Address); err != nil {
				if *debug == DebugCode {
					log.Printf("check address: %v: %v", cur, err)
				}
				time.Sleep(time.Second)
			} else {
				available = true
				break
			}
		}

		if available {
			// update the status to up if it's down before
			if cur.Status == StatusDown {
				for i := 0; i < Retries; i++ {
					if err := update(cur, StatusUP); err != nil {
						log.Printf("update status: %v", err)
					} else {
						break
					}
					if i+1 < Retries {
						time.Sleep(time.Duration((i + 1)) * time.Second)
					}
				}
			}
		} else {
			// update the status to down if it's up before
			if cur.Status == StatusUP {
				for i := 0; i < Retries; i++ {
					if err := update(cur, StatusDown); err != nil {
						log.Printf("update status: %v", err)
					} else {
						break
					}
					if i+1 < Retries {
						time.Sleep(time.Duration((i + 1)) * time.Second)
					}
				}
			}
		}

		offset++
		if offset == end {
			offset = start
		}

		// sleep a short after pinging a dns
		time.Sleep(delay)
	}
}

func authorize() error {
	params := url.Values{}
	params.Add("s", *serial)
	body := strings.NewReader(params.Encode())

	var res Response
	if err := doRequest(AuthURL, body, &res); err != nil {
		return err
	}
	if res.Result != Success {
		return fmt.Errorf("authorize: %v", res.Result)
	}

	return nil
}

func main() {
	flag.Parse()

	// print the command parameters
	log.Printf("parameters: members=%v, sleep time=%vms, interval=%vs", *members, *wait, *interval)

	// check if the serial is ready
	if *serial == "" {
		// read the serial from "/proc/cpuinfo"
		script := "cat /proc/cpuinfo | grep --ignore-case serial | awk '{ print $3 }'"
		output, err := runScript(script)
		if err != nil {
			log.Fatalf("run script: %v, %v", script, err)
		}
		if output == "" {
			log.Fatalf("the serial number is empty")
		}
		*serial = output
	}

	// do the request for authorization with 'rp/h'
	if err := authorize(); err != nil {
		log.Fatalf("do authorization: %v", err)
	}

	// wait group for managing the goroutines
	var wg sync.WaitGroup

	wg.Add(1)
	// start a goroutines for handling the synced tasks
	go handleTasks(&wg)

	// start a timer job for dumping the runtime
	if err := startTimerJob(&wg, done, "dump runtime", 15*time.Second, false, func() error {
		log.Printf("number of running goroutines for checking dns: 【%d】", goroutines)
		return nil
	}); err != nil {
		log.Fatalf("start the timer for dumping the runtime: %v", err)
	}

	// start a timer job for syncing the dns list
	if err := startTimerJob(&wg, done, "dns list", time.Duration(*interval)*time.Second, true, func() error {
		var res Response
		if err := doRequest(FetchURL, nil, &res); err != nil {
			return err
		}
		if res.Result == Success {
			newTask := []*Status{}
			for key, dns := range res.List {
				id, _ := strconv.Atoi(key)
				newTask = append(newTask, &Status{
					ID:      id,
					Address: dns.Address,
					Status:  dns.Status,
				})
			}
			// sort the dns list by id
			sort.Slice(newTask, func(i, j int) bool {
				return newTask[i].ID <= newTask[j].ID
			})

			tasks <- newTask
		} else if res.Result == "error" {
			// it needs to do authorization again
			if err := authorize(); err != nil {
				log.Printf("do authorization after fetching dns: %v", err)
			}
		} else {
			log.Printf("do request for %s: %v", FetchURL, res.Result)
		}

		return nil
	}); err != nil {
		log.Fatalf("start the timer for fetching dns list: %v", err)
	}

	// // start a timer job for authorizing the checker (no need right now)
	// if err := startTimerJob(&wg, done, "authorization", time.Duration(*verify)*time.Second, true, func() error {
	// 	params := url.Values{}
	// 	params.Add("s", *serial)
	// 	body := strings.NewReader(params.Encode())

	// 	var res Response
	// 	if err := doRequest(AuthURL, body, &res); err != nil {
	// 		return err
	// 	}
	// 	if res.Result != Success {
	// 		log.Printf("do request for %s: %v", AuthURL, res.Result)
	// 	}

	// 	return nil
	// }); err != nil {
	// 	log.Fatalf("start the timer for authorization: %v", err)
	// }

	log.Printf("dns checker is started")

	sig := make(chan os.Signal, 1024)
	// subscribe signals: SIGINT & SINGTERM
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	for o := range sig {
		log.Printf("receive signal: %v", o)

		start := time.Now()

		// stop the goroutines
		if done != nil {
			close(done)
		}
		if pointer != nil {
			close(pointer)
		}

		// wait for goroutines are done
		wg.Wait()

		log.Printf("dns checker is stopped, takes time: %v", time.Since(start))

		// close the signal to exit
		close(sig)
	}
}
