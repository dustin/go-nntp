package nntp

type PostingStatus byte

const (
	Unknown             = PostingStatus(0)
	PostingPermitted    = PostingStatus('y')
	PostingNotPermitted = PostingStatus('n')
	PostingModerated    = PostingStatus('m')
)

type Group struct {
	Name    string
	Count   int64
	High    int64
	Low     int64
	Posting PostingStatus
}
