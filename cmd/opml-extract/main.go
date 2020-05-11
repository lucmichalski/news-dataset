package main

import (
	//"bufio"
	//"fmt"
	//"os"
	"strings"

	"github.com/gilliek/go-opml/opml"
	"github.com/k0kubun/pp"
	"github.com/lucmichalski/news-dataset/pkg/gofeed"
	"github.com/nozzle/throttler"
	log "github.com/sirupsen/logrus"
)

func main() {

	doc, err := opml.NewOPMLFromFile("12k.opml")
	if err != nil {
		log.Fatal(err)
	}

	csvFeed, err := NewCsvWriter("dataset_feed.csv")
	if err != nil {
		panic("Could not open `dataset_feed.csv` for writing")
	}
	defer csvFeed.Close()
	csvFeed.Write([]string{"link", "feedlink", "title", "categories"})
	csvFeed.Flush()

	csvArticle, err := NewCsvWriter("dataset.csv")
	if err != nil {
		panic("Could not open `dataset.csv` for writing")
	}
	defer csvArticle.Close()
	csvArticle.Write([]string{"link", "title", "categories"})
	csvArticle.Flush()

	t := throttler.New(12, len(doc.Body.Outlines))
	for _, b := range doc.Body.Outlines {

		go func(b opml.Outline) error {
			// Let Throttler know when the goroutine completes
			// so it can dispatch another worker
			defer t.Done(nil)

			url := getXMLURLFromOPML(b)
			fp := gofeed.NewParser()
			log.Println("feed: ", url)
			feed, err := fp.ParseURL(url)
			if err != nil {
				return err
			}

			if len(feed.Categories) > 0 {
				pp.Println(feed.Categories)
				cats := strings.Join(feed.Categories, ";")
				csvFeed.Write([]string{feed.Link, feed.FeedLink, feed.Title, cats})
			}

			// topics = append(topics, feed.Categories...)
			// topics = removeDuplicates(topics)
			// doc.Body.Outlines[i].Category = strings.Join(topics, ",")
			// pp.Println("categories: ", topics)

			for _, feed := range feed.Items {
				pp.Println(feed.Title)
				pp.Println(feed.Link)
				pp.Println(feed.Categories)
				cats := strings.Join(feed.Categories, ";")
				if len(cats) > 0 {
					pp.Println(cats)
					csvArticle.Write([]string{feed.Link, feed.Title, cats})
				}
			}
			csvFeed.Flush()
			csvArticle.Flush()
			return nil

		}(b)
		t.Throttle()

	}

	// throttler errors iteration
	if t.Err() != nil {
		// Loop through the errors to see the details
		for i, err := range t.Errs() {
			log.Printf("error #%d: %s", i, err)
		}
		log.Fatal(t.Err())
	}

}

func removeDuplicates(elements []string) []string {
	// Use map to record duplicates as we find them.
	encountered := map[string]bool{}
	result := []string{}

	for v := range elements {
		elements[v] = strings.ToLower(elements[v])
		if encountered[elements[v]] == true {
			// Do not add duplicate.
		} else {
			// Record this element as an encountered element.
			encountered[elements[v]] = true
			// Append to result slice.
			result = append(result, elements[v])
		}
	}
	// Return the new slice.
	return result
}

func check(e error) {
	if e != nil {
		panic(e)
	}
}

func getXMLURLFromOPML(b opml.Outline) string {
	str := ""
	if b.XMLURL != "" {
		str = b.XMLURL
	}
	return str
}
