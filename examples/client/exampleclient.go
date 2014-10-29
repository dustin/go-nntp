package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/dustin/go-nntp/client"
)

const examplepost = `From: <nobody@example.com>
Newsgroups: misc.test
Subject: Code test
Organization: spy internetworking

This is a test post.
`

func maybefatal(s string, e error) {
	if e != nil {
		log.Fatalf("Error in %s: %v", s, e)
	}
}

func main() {
	server, user, pass := os.Args[1], os.Args[2], os.Args[3]
	c, err := nntpclient.New("tcp", server)
	maybefatal("connecting", err)
	log.Printf("Got banner:  %v", c.Banner)

	// Authenticate
	msg, err := c.Authenticate(user, pass)
	maybefatal("authenticating", err)
	log.Printf("Post authentication message:  %v", msg)

	// Set the reader mode
	_, _, err = c.Command("mode reader", 2)
	maybefatal("setting reader mode", err)

	g, err := c.Group("misc.test")
	maybefatal("grouping", err)
	log.Printf("Got %#v", g)

	n, id, r, err := c.Head(fmt.Sprintf("%v", g.High-1))
	maybefatal("getting head", err)
	log.Printf("msg %d has id %v and the following headers", n, id)
	io.Copy(os.Stdout, r)

	n, id, r, err = c.Body(fmt.Sprintf("%v", n))
	maybefatal("getting body", err)
	log.Printf("Body of message %v", id)
	io.Copy(os.Stdout, r)

	n, id, r, err = c.Article(fmt.Sprintf("%v", n))
	maybefatal("getting the whole thing", err)
	log.Printf("Full message %v", id)
	io.Copy(os.Stdout, r)

	err = c.Post(strings.NewReader(examplepost))
	maybefatal("posting", err)
	log.Printf("Posted!")
}
