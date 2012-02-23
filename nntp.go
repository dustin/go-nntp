// NNTP definitions.
package nntp

import (
	"fmt"
	"io"
	"net/textproto"
)

// Posting status type for groups.
type PostingStatus byte

const (
	Unknown             = PostingStatus(0)
	PostingPermitted    = PostingStatus('y')
	PostingNotPermitted = PostingStatus('n')
	PostingModerated    = PostingStatus('m')
)

func (ps PostingStatus) String() string {
	return fmt.Sprintf("%c", ps)
}

// A usenet newsgroup.
type Group struct {
	Name        string
	Description string
	Count       int64
	High        int64
	Low         int64
	Posting     PostingStatus
}

// An article that may appear in one or more groups.
type Article struct {
	// The article's headers
	Header textproto.MIMEHeader
	// The article's body
	Body io.Reader
	// Number of bytes in the article body (used by OVER/XOVER)
	Bytes int
	// Number of lines in the article body (used by OVER/XOVER)
	Lines int
}

// Convenient access to the article's Message ID.
func (a *Article) MessageId() string {
	return a.Header.Get("Message-Id")
}
