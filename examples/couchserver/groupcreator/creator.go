package main

import (
	"bufio"
	"flag"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/dustin/go-couch"
)

var wg sync.WaitGroup

type agroup struct {
	Type        string `json:"type"`
	Name        string `json:"_id"`
	Description string `json:"description"`
}

func process(db *couch.Database, line string) {
	defer wg.Done()
	parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
	if len(parts) != 2 {
		log.Printf("Error parsing %v", line)
		return
	}
	log.Printf("Processing %#v", parts)
	g := agroup{
		Type:        "group",
		Name:        parts[0],
		Description: parts[1],
	}
	_, _, err := db.Insert(g)
	if err != nil {
		log.Printf("Error saving %#v: %v", g, err)
	}
}

func main() {

	couchUrl := flag.String("couch", "http://localhost:5984/news",
		"Couch DB.")
	flag.Parse()

	db, err := couch.Connect(*couchUrl)
	if err != nil {
		log.Fatalf("Can't connect to couch: %v", err)
	}

	br := bufio.NewReader(os.Stdin)
	for {
		line, err := br.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("Error reading line: %v", err)
		}

		wg.Add(1)
		go process(&db, line)
	}

	wg.Wait()
}
