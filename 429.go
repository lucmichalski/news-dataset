package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gocolly/colly/v2"
	// "github.com/gocolly/colly/v2/proxy"
)

var (
	cachePath = "./data/cache"
)

func main() {

	// Instantiate default collector
	c := colly.NewCollector(
		// Attach a debugger to the collector
		// colly.Debugger(&debug.LogDebugger{}),
		colly.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_4) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/81.0.4044.138 Safari/537.36"),
		colly.Async(true),
		colly.CacheDir(cachePath),
	)

	/*
		rp, err := proxy.RoundRobinProxySwitcher("http://144.217.101.242:3129", "http://118.163.83.21:3128", "http://94.177.227.6:8080")
		if err != nil {
			log.Fatal(err)
		}
		c.SetProxyFunc(rp)
	*/

	// Limit the number of threads started by colly to two
	// when visiting links which domains' matches "*httpbin.*" glob
	c.Limit(&colly.LimitRule{
		DomainGlob:  "*medium.*",
		Parallelism: 1,
		RandomDelay: 15 * time.Second,
	})

	c.OnError(func(r *colly.Response, err error) {
		fmt.Println("error:", err, r.Request.URL, r.StatusCode)
	})

	// Print the response
	c.OnResponse(func(r *colly.Response) {
		log.Printf("Proxy Address: %s\n", r.Request.ProxyURL)
		log.Printf("%s\n", bytes.Replace(r.Body, []byte("\n"), nil, -1))
	})

	if _, err := os.Stat("medium_429.csv"); !os.IsNotExist(err) {
		file, err := os.Open("medium_429.csv")
		if err != nil {
			log.Fatal(err)
		}

		reader := csv.NewReader(file)
		reader.Comma = ','
		reader.LazyQuotes = true
		data, err := reader.ReadAll()
		if err != nil {
			log.Fatal(err)
		}

		// shuffle(data)
		for _, loc := range data {
			c.Visit(loc[0])
			time.Sleep(5 * time.Second)
		}
	}

	// Wait until threads are finished
	c.Wait()
}
