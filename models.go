package main

import (
	"bytes"
	"encoding/json"
	"github.com/jmoiron/sqlx/types"
	"github.com/shurcooL/go/github_flavored_markdown"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var CardLinkMatcherExpression = "\\]\\(https?://trello.com/c/([^/]+)(/[\\w-]*)?\\)"
var CardLinkMatcher *regexp.Regexp

func renderMarkdown(md string) string {
	mdBytes := []byte(md)
	mdBytes = CardLinkMatcher.ReplaceAllFunc(mdBytes, func(match []byte) []byte {
		shortLink := append(CardLinkMatcher.FindSubmatch(match)[1], ")"...)
		return append([]byte("](/c/"), shortLink...)
	})
	html := github_flavored_markdown.Markdown(mdBytes)
	return string(html)
}

type RequestData struct {
	error
	Request          *http.Request
	BaseURL          *url.URL
	Board            Board
	Lists            []List
	Aggregator       Aggregator
	Cards            []Card
	Card             Card
	Page             int
	SearchQuery      string
	TypedSearchQuery bool
	SearchResults    SearchResults
	HasNext          bool
	HasPrev          bool
	Prefs            Preferences
	Settings         Settings
	ShowMF2          bool
	Content          string
}

func (b RequestData) NavItems() []Link {
	var lists []Link
	var navItems []Link
	for _, list := range b.Lists {
		lists = append(lists, Link{
			Text: list.Name,
			Url:  "/" + list.Slug + "/",
		})
	}
	for _, link := range b.Prefs.Nav {
		if link.Text == "__lists__" {
			navItems = append(navItems, lists...)
		} else {
			navItems = append(navItems, link)
		}
	}
	return navItems
}

func (b RequestData) NextPage() int {
	return b.Page + 1
}

func (b RequestData) PrevPage() int {
	return b.Page - 1
}

type Preferences struct {
	Header struct {
		Text  string
		Image string
	}
	Comments struct {
		Display     bool
		Box         bool
		Webmentions bool
	}
	Aside             string
	Favicon           string
	Domain            string
	Includes          []string
	Nav               []Link
	PostsPerPageValue string `json:"posts-per-page"`
	ExcerptsValue     string `json:"excerpts"`
}

func (prefs Preferences) HasHeaderImage() bool {
	if prefs.Header.Image != "" {
		return true
	}
	return false
}

func (prefs Preferences) AsideRender() string {
	return renderMarkdown(prefs.Aside)
}

func (prefs Preferences) JS() []string {
	js := make([]string, 0)
	for _, incl := range prefs.Includes {
		u, err := url.Parse(incl)
		if err != nil {
			continue
		}
		if strings.HasSuffix(u.Path, ".js") {
			js = append(js, incl)
		}
	}
	return js
}

func (prefs Preferences) CSS() []string {
	css := make([]string, 0)
	for _, incl := range prefs.Includes {
		u, err := url.Parse(incl)
		if err != nil {
			continue
		}
		if strings.HasSuffix(u.Path, ".css") {
			css = append(css, incl)
		}
	}
	return css
}

func (prefs Preferences) PostsPerPage() int {
	ppp, err := strconv.Atoi(prefs.PostsPerPageValue)
	if err != nil {
		return 7
	}
	if ppp > 15 {
		return 15
	}
	return ppp
}

func (prefs Preferences) Excerpts() int {
	limit, err := strconv.Atoi(prefs.ExcerptsValue)
	if err != nil {
		return 0
	}
	if limit > 300 {
		return 300
	}
	return limit
}

func (prefs Preferences) ShowExcerpts() bool {
	if prefs.Excerpts() > 0 {
		return true
	}
	return false
}

type SearchResults []Card

func (o SearchResults) Some() bool {
	if len(o) > 0 {
		return true
	}
	return false
}

func (o SearchResults) Len() int {
	return len(o)
}

type Link struct {
	Text string
	Url  string
}

type Board struct {
	Id   string
	Name string
	Desc string
}

type User struct {
	Id       string `db:"_id" json:"_id"`
	Username string `db:"id" json:"id"`
}

func (o Board) DescRender() string {
	return renderMarkdown(o.Desc)
}

type Aggregator interface {
	Test() interface{}
}

type List struct {
	Id   string
	Name string
	Slug string
	Pos  int
}

type Label struct {
	Id    string
	Name  string
	Slug  string
	Color string
}

func (o Label) NameOrSpaces() string {
	if o.Name == "" {
		return "       "
	}
	return o.Name
}

func (o Label) SlugOrId() string {
	if o.Slug == "" {
		return o.Id
	}
	return o.Slug
}

type Card struct {
	Id          string
	ShortLink   string `db:"shortLink"`
	Name        string
	PageTitle   string `db:"pageTitle"`
	Slug        string
	Cover       string
	Desc        string
	Excerpt     string
	Due         interface{}
	Comments    []Comment
	List_id     string
	Users       types.JsonText
	Labels      types.JsonText
	Checklists  types.JsonText
	Attachments types.JsonText
	IsPage      bool
	Color       string // THIS IS JUST FOR DISGUISING LABELS AS CARDS
}

func (card Card) DescRender() string {
	return renderMarkdown(card.Desc)
}

func (card Card) HasExcerpt() bool {
	if strings.TrimSpace(card.Excerpt) == "" {
		return false
	}
	return true
}

func (card Card) HasCover() bool {
	if card.Cover == "" {
		return false
	}
	return true
}

func (card Card) GetChecklists() []Checklist {
	var checklists []Checklist
	if !bytes.Equal(card.Checklists, nil) {
		err := json.Unmarshal(card.Checklists, &checklists)
		if err != nil {
			log.Print("Problem unmarshaling checklists JSON")
			log.Print(err)
			log.Print(string(card.Checklists[:]))
		}
	}
	return checklists
}

func (card Card) GetAttachments() []Attachment {
	var attachments []Attachment
	if !bytes.Equal(card.Attachments, nil) {
		err := json.Unmarshal(card.Attachments, &attachments)
		if err != nil {
			log.Print("Problem unmarshaling attachments JSON")
			log.Print(err)
			log.Print(string(card.Attachments[:]))
		}
	}
	return attachments
}

func (card Card) HasAttachments() bool {
	attachments := card.GetAttachments()
	if len(attachments) > 0 {
		return true
	}
	return false
}

func (card Card) AuthorHTML() string {
	var users []User
	if !bytes.Equal(card.Users, nil) {
		err := json.Unmarshal(card.Users, &users)
		if err != nil {
			log.Print("Problem unmarshaling users JSON")
			log.Print(err)
			log.Print(string(card.Users))
		}
	}

	if len(users) == 0 {
		return ""
	} else if len(users) == 1 {
		return `<address><a rel="author" target="_blank" href="https://trello.com/` + users[0].Id + `">` + users[0].Username + `</a></address>`
	} else if len(users) == 2 {
		return `<address><a rel="author" target="_blank" href="https://trello.com/` + users[0].Id + `">` + users[0].Username + `</a> & <a href="https://trello.com/` + users[1].Id + `" "target="_blank">` + users[1].Username + `</a></address>`
	} else {
		return `<address><a rel="author" target="_blank" href="https://trello.com/` + users[0].Id + `">` + users[0].Username + `</a> et al.</address>`
	}
}

func (card Card) GetLabels() []Label {
	var labels []Label
	if !bytes.Equal(card.Labels, nil) {
		err := json.Unmarshal(card.Labels, &labels)
		if err != nil {
			log.Print("Problem unmarshaling labels JSON")
			log.Print(err)
			log.Print(string(card.Labels[:]))
		}
	}
	return labels
}

type Checklist struct {
	Name       string
	CheckItems []CheckItem
}

type CheckItem struct {
	State string
	Name  string
}

func (c CheckItem) Complete() bool {
	return c.State == "complete"
}
func (o CheckItem) NameRender() string {
	return renderMarkdown(o.Name)
}

type Attachment struct {
	Name      string
	Url       string
	EdgeColor string
}

type Comment struct {
	Id            string
	AuthorName    string `db:"author_name"`
	AuthorURL     string `db:"author_url"`
	Body          string
	SourceDisplay string `db:"source_display"`
	SourceURL     string `db:"source_url"`
}

func (comment Comment) BodyRender() string {
	return renderMarkdown(comment.Body)
}

/* mustache helpers */
func (comment Comment) Date() time.Time {
	unix, err := strconv.ParseInt(comment.Id[:8], 16, 0)
	if err != nil {
		return time.Now()
	}
	return time.Unix(unix, 0)
}

func (comment Comment) PrettyDate() string {
	date := comment.Date()
	return date.Format("2 Jan 2006")
}

func (comment Comment) IsoDate() string {
	date := comment.Date()
	return date.Format("2006-01-02T15:04:05.999")
}

func (card Card) Date() time.Time {
	if card.Due != nil {
		return card.Due.(time.Time)
	} else {
		unix, err := strconv.ParseInt(card.Id[:8], 16, 0)
		if err != nil {
			return time.Now()
		}
		return time.Unix(unix, 0)
	}
}

func (card Card) PrettyDate() string {
	date := card.Date()
	return date.Format("2 Jan 2006")
}

func (card Card) IsoDate() string {
	date := card.Date()
	return date.Format("2006-01-02T15:04:05.999")
}

func (o Label) Test() interface{} {
	if o.Slug != "" {
		return o
	}
	return false
}

func (o List) Test() interface{} {
	if o.Slug != "" {
		return o
	}
	return false
}

func (o Card) Test() interface{} {
	if o.Slug != "" {
		return o
	}
	return false
}
