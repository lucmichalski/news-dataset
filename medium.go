package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"
	"strconv"

	"github.com/qor/media"
	"github.com/qor/validations"
	"github.com/jinzhu/gorm"
  	_ "github.com/mattn/go-sqlite3"
 	"github.com/qor/admin"
	"github.com/beevik/etree"
	badger "github.com/dgraph-io/badger"
	"github.com/gilliek/go-opml/opml"
	"github.com/gocolly/colly/v2"
	"github.com/gocolly/colly/v2/queue"
	"github.com/golang/snappy"
	"github.com/k0kubun/pp"
	"github.com/mmcdole/gofeed"
	"github.com/nozzle/throttler"
	cmap "github.com/orcaman/concurrent-map"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/abadojack/whatlanggo"
)

var (
	store          *badger.DB
	isHelp         bool
	isVerbose      bool
	isAdmin        bool
	parallelJobs   int
	queueMaxSize   = 100000000
	cachePath      = "./data/cache"
	storagePath    = "./data/badger"
	opmlFile       = "./sniperkit.opml"
	sitemapRootURL = "https://medium.com/sitemap/sitemap.xml"
)

type feed struct {
  	gorm.Model
  	Title string
  	Description string `gorm:"type:longtext;"`
  	Link string `gorm:"size:255;unique"`
	FeedLink string
	Updated string
	Published string
  	AuthorName string
  	AuthorEmail string	
	Language string	
  	ImageUrl string
  	ImageTitle string
	Copyright string
	Generator string
	DetectLang string 
	DetectLangConfidence float64
	Categories []category `gorm:"many2many:feed_categories;"`
	CategoriesStr string
}

type category struct {
  	gorm.Model
	Name string
}

type article struct {
  	gorm.Model
  	Title        string
  	Description string `gorm:"type:longtext;"`
  	Content string
  	Link string `gorm:"size:255;unique"`
  	Updated string
  	Published string
  	AuthorName string
  	AuthorEmail string
  	Guid string
  	ImageUrl string
  	ImageTitle string
	DetectLang string 
	DetectLangConfidence float64
  	Categories []category `gorm:"many2many:article_categories;"`
	CategoriesStr string
  	Enclosures string
}

func main() {
	pflag.IntVarP(&parallelJobs, "parallel-jobs", "j", 1, "parallel jobs.")
	pflag.BoolVarP(&isAdmin, "admin", "a", false, "launch web admin.")
	pflag.BoolVarP(&isVerbose, "verbose", "v", false, "verbose mode.")
	pflag.BoolVarP(&isHelp, "help", "h", false, "help info.")
	pflag.Parse()
	if isHelp {
		pflag.PrintDefaults()
		os.Exit(1)
	}

	// Instanciate the mysql client
	DB, err := gorm.Open("sqlite3", "medium.db")
	// DB, err := gorm.Open("mysql", fmt.Sprintf("%v:%v@tcp(%v:%v)/%v?charset=utf8mb4,utf8&parseTime=True", os.Getenv("PI_MYSQL_USER"), os.Getenv("PI_MYSQL_PASSWORD"), os.Getenv("PI_MYSQL_HOST"), os.Getenv("PI_MYSQL_PORT"), "dataset_news"))
	if err != nil {
		log.Fatal(err)
	}
	defer DB.Close()

	// callback for images and validation
	validations.RegisterCallbacks(DB)
	media.RegisterCallbacks(DB)

	DB.AutoMigrate(&category{})
	DB.AutoMigrate(&feed{})
	DB.AutoMigrate(&article{})

	if isAdmin {
		// Initialize
		Admin := admin.New(&admin.AdminConfig{DB: DB})

		// Allow to use Admin to manage User, Product
		Admin.AddResource(&article{})
		Admin.AddResource(&category{})
		Admin.AddResource(&feed{})

		// initalize an HTTP request multiplexer
		mux := http.NewServeMux()

		// Mount admin interface to mux
		Admin.MountTo("/admin", mux)

		fmt.Println("Listening on: 9000")
		http.ListenAndServe(":9000", mux)
	}


	csvSitemap, err := NewCsvWriter("medium_dataset.csv")
	if err != nil {
		panic("Could not open `csvSitemap.csv` for writing")
	}

	// Flush pending writes and close file upon exit of Sitemap()
	defer csvSitemap.Close()

	csvSitemap.Write([]string{"link", "categories", "title", "description", "content", "language", "language_confidence"})
	csvSitemap.Flush()

	// new concurrent map
	m := cmap.New()

	// init storage path
	err = ensureDir(storagePath)
	if err != nil {
		log.Fatal(err)
	}
	store, err = badger.Open(badger.DefaultOptions(storagePath))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	// medium user regex pattern
	userMediumPatternRegexp, err := regexp.Compile(`https://medium.com/@[A-Za-z0-9.-]+`)
	if err != nil {
		log.Fatal(err)
	}

	// Instantiate default collector
	c := colly.NewCollector(
		//colly.UserAgent(uarand.GetRandom()),
		colly.CacheDir(cachePath),
		colly.URLFilters(
			regexp.MustCompile("https://medium\\.com/sitemap/users/(.*)"),
			regexp.MustCompile("https://medium.com/@[A-Za-z.-]+"),
		),
	)

	// sitemap/users/

	// create a request queue with 1 consumer thread
	q, _ := queue.New(
		parallelJobs, // Number of consumer threads set to 1 to avoid dead lock on database
		&queue.InMemoryQueueStorage{
			MaxSize: queueMaxSize,
		}, // Use default queue storage
	)

	// c.DisableCookies()

	// Create a callback on the XPath query searching for the URLs
	c.OnXML("//sitemap/loc", func(e *colly.XMLElement) {
		q.AddURL(e.Text)
		log.Println("//sitemap/loc", e.Text)
	})

	// Create a callback on the XPath query searching for the URLs
	c.OnXML("//urlset/url/loc", func(e *colly.XMLElement) {
		match := userMediumPatternRegexp.FindString(e.Text)
		if match != "" {
			userFeedUrl := strings.Replace(match, "https://medium.com/", "https://medium.com/feed/", -1)
			log.Println("userFeedUrl", userFeedUrl)
			m.Set(userFeedUrl, true)
		} else {
			log.Println("skipping", e.Text)
		}
	})

	c.OnError(func(r *colly.Response, err error) {
		fmt.Println("error:", err, r.Request.URL, r.StatusCode)
		q.AddURL(r.Request.URL.String())
	})

	c.OnResponse(func(r *colly.Response) {
		if isVerbose {
			fmt.Println("OnResponse from", r.Ctx.Get("url"))
		}
	})

	// Before making a request print "Visiting ..."
	c.OnRequest(func(r *colly.Request) {
		//if isVerbose {
		fmt.Println("Visiting", r.URL.String())
		//}
		r.Ctx.Put("url", r.URL.String())
	})

	// Start scraping on https://www.autosphere.fr
	log.Infoln("extractSitemapIndex...")
	sitemaps, err := extractSitemapIndex(sitemapRootURL)
	if err != nil {
		log.Fatal("ExtractSitemapIndex:", err)
	}

	shuffle(sitemaps)
	for _, sitemap := range sitemaps {
		log.Infoln("processing ", sitemap)
		if strings.Contains(sitemap, ".gz") {
			log.Infoln("extract sitemap gz compressed...")
			locs, err := extractSitemapGZ(sitemap)
			if err != nil {
				log.Fatal("ExtractSitemapGZ", err)
			}
			shuffle(locs)
			for _, loc := range locs {
				q.AddURL(loc)
			}
		} else {
			q.AddURL(sitemap)
		}
	}

	// Consume URLs
	q.Run(c)

	// curl -H "User-Agent: Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)" https://www.leboncoin.fr/sitemap_index.xml

	log.Println("Collected cmap: ", m.Count(), "users")

	time.Sleep(10 * time.Second)

	t := throttler.New(3, m.Count())

	m.IterCb(func(key string, v interface{}) {

		go func(key string) error {
			// Let Throttler know when the goroutine completes
			// so it can dispatch another worker
			defer t.Done(nil)

			pp.Println("new key: ", key)
			fp := gofeed.NewParser()
			fee, err := fp.ParseURL(key)
			if err != nil {
				log.Warn(err)
				return err
			}
	
			f := &feed{
			  	Title: fee.Title,
			  	Description: fee.Description,
			  	Link: fee.Link,
				FeedLink: fee.FeedLink,
				Updated: fee.Updated,
				Published: fee.Published, 
				AuthorName:	fee.Author.Name,
				AuthorEmail: fee.Author.Email,
				Language: fee.Language,	
				ImageTitle: fee.Image.Title,
				ImageUrl: fee.Image.URL,
				Copyright: fee.Copyright,
				Generator: fee.Generator,
			}

			// categories
			var sliceCats []string
			cats := make([]category, len(fee.Categories))
			for _, cat := range fee.Categories {
				sliceCats = append(sliceCats, cat)
				c := &category{Name: cat}
				cat, err := createOrUpdateCategory(DB, c)
				if err != nil {
					log.Fatalln("error while createOrUpdateCategory, msg:", err)
				}
				cats = append(cats, *cat)
			}

			// f.Categories = cats
			f.CategoriesStr = strings.Join(sliceCats, ",")

			info := whatlanggo.Detect(fee.Title + " " + strings.Join(sliceCats, " "))
			fmt.Println("Language:", info.Lang.String(), " Script:", whatlanggo.Scripts[info.Script], " Confidence: ", info.Confidence)

			f.DetectLang = info.Lang.String()
			f.DetectLangConfidence = info.Confidence

			DB.Create(&f)

			pp.Println(f)
			// os.Exit(1)

			for _, item := range fee.Items {

				a := &article{
					Title: item.Title,
					Description: item.Description,
					Content: item.Content,
					Link: item.Link,
					Updated: item.Updated,
					Published: item.Published,
					Guid: item.GUID,
					// Enclosures: item.Enclosures,					
				}

				if item.Author != nil {
					a.AuthorName =  item.Author.Name
					a.AuthorEmail = item.Author.Email
				}

				if item.Image != nil {
					a.ImageUrl = item.Image.URL
					a.ImageTitle = item.Image.Title
				}

				var sliceCats []string
				for _, cat := range item.Categories {
					sliceCats = append(sliceCats, cat)
					c := &category{Name: cat}
					cat, err := createOrUpdateCategory(DB, c)
					if err != nil {
						log.Fatalln("error while createOrUpdateCategory, msg:", err)
					}
					cats = append(cats, *cat)
				}
				// a.Categories = cats
				a.CategoriesStr = strings.Join(sliceCats, ",")

				info := whatlanggo.Detect(item.Title + " " + item.Description + " " + item.Content + " " + strings.Join(sliceCats, " "))
				fmt.Println("Language:", info.Lang.String(), " Script:", whatlanggo.Scripts[info.Script], " Confidence: ", info.Confidence)

				a.DetectLang = info.Lang.String()
				a.DetectLangConfidence = info.Confidence

				// save article
				DB.Create(&a)

				cats := strings.Join(item.Categories, ",")
				if len(cats) > 0 {
					langConfidence := strconv.FormatFloat(info.Confidence, 'f', -1, 64)
					csvSitemap.Write([]string{item.Link, cats, item.Title, item.Description, item.Content, info.Lang.String(), langConfidence})
				}

			}

			time.Sleep(1 * time.Second)

			return nil
		}(key)
		t.Throttle()
	})

	// throttler errors iteration
	if t.Err() != nil {
		// Loop through the errors to see the details
		for i, err := range t.Errs() {
			log.Warnf("error #%d: %s", i, err)
		}
		// log.Fatal(t.Err())
	}

}

func createOrUpdateCategory(db *gorm.DB, cat *category) (*category, error) {
	var existingCategory category
	if db.Where("name = ?", cat.Name).First(&existingCategory).RecordNotFound() {
		err := db.Create(cat).Error
		return cat, err
	}
	cat.ID = existingCategory.ID
	cat.CreatedAt = existingCategory.CreatedAt
	return cat, nil
}

type fpQuery struct {
	url  string
	tags string
}

func addFeed(url string) error {
	timeout := time.Duration(2 * time.Second)
	client := http.Client{
		Timeout: timeout,
	}

	fpQuery := &fpQuery{
		url: url,
	}

	requestBody, err := json.Marshal(fpQuery)
	if err != nil {
		// fatal(w, log, "Error while generating payload to yake: %+v", err)
		return err
	}

	request, err := http.NewRequest("POST", "http://localhost:8080/v2/feeds", bytes.NewBuffer(requestBody))
	// request.Header.Set("Content-type", "application/json")
	if err != nil {
		//fatal(w, log, "Error building new request to yake: %+v", err)
		return err
	}
	resp, err := client.Do(request)
	if err != nil {
		//warn(w, log, "Error while getting response from yake service: %+v", err)
		return err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		// warn(w, log, "Error while reading response from yake service: %+v", err)
		return err
	}
	log.Println(string(body))
	return nil
}

func shuffle(slice interface{}) {
	rv := reflect.ValueOf(slice)
	swap := reflect.Swapper(slice)
	length := rv.Len()
	for i := length - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		swap(i, j)
	}
}

func extractSitemapIndex(url string) ([]string, error) {
	client := new(http.Client)
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer response.Body.Close()

	doc := etree.NewDocument()
	if _, err := doc.ReadFrom(response.Body); err != nil {
		return nil, err
	}
	var urls []string
	index := doc.SelectElement("sitemapindex")
	sitemaps := index.SelectElements("sitemap")
	for _, sitemap := range sitemaps {
		loc := sitemap.SelectElement("loc")
		log.Infoln("loc:", loc.Text())
		urls = append(urls, loc.Text())
	}
	return urls, nil
}

func extractSitemapGZ(url string) ([]string, error) {
	client := new(http.Client)
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer response.Body.Close()

	var reader io.ReadCloser
	reader, err = gzip.NewReader(response.Body)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	defer reader.Close()

	doc := etree.NewDocument()
	if _, err := doc.ReadFrom(reader); err != nil {
		panic(err)
	}
	var urls []string
	urlset := doc.SelectElement("urlset")
	entries := urlset.SelectElements("url")
	for _, entry := range entries {
		loc := entry.SelectElement("loc")
		log.Infoln("loc:", loc.Text())
		urls = append(urls, loc.Text())
	}
	return urls, err
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

func getFromBadger(key string) (resp []byte, ok bool) {
	err := store.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		err = item.Value(func(val []byte) error {
			// This func with val would only be called if item.Value encounters no error.
			// Accessing val here is valid.
			// fmt.Printf("The answer is: %s\n", val)
			return nil
		})
		if err != nil {
			return err
		}
		return nil
	})
	return resp, err == nil
}

func addToBadger(key, value string) error {
	err := store.Update(func(txn *badger.Txn) error {
		if isVerbose {
			log.Println("indexing: ", key)
		}
		cnt, err := compress([]byte(value))
		if err != nil {
			return err
		}
		err = txn.Set([]byte(key), cnt)
		return err
	})
	return err
}

func compress(data []byte) ([]byte, error) {
	return snappy.Encode([]byte{}, data), nil
}

func decompress(data []byte) ([]byte, error) {
	return snappy.Decode([]byte{}, data)
}

func ensureDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		os.MkdirAll(path, os.FileMode(0755))
	} else {
		return err
	}
	d.Close()
	return nil
}

