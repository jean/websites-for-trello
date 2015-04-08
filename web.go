package main

import (
	"fmt"
	"github.com/gorilla/mux"
	"github.com/jabley/mustache"
	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/types"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	_ "github.com/lib/pq"
)

var db *sqlx.DB
var settings Settings

func getBaseData(w http.ResponseWriter, r *http.Request) BaseData {
	var err error
	var identifier string

	var board Board
	var author Author

	// board and author
	if strings.HasSuffix(r.Host, settings.Domain) {
		// subdomain
		identifier = strings.Split(r.Host, ".")[0]
		err = db.Get(&board, `
SELECT boards.id, name, boards.desc, users.id AS user_id, 'avatarHash', 'gravatarHash', users.bio
FROM boards
INNER JOIN users ON users.id = boards.user_id
WHERE subdomain = $1`,
			identifier)
	} else {
		// domain
		identifier = r.Host
		err = db.Get(&board, `
SELECT boards.id, name, boards.desc, users.id AS user_id, 'avatarHash', 'gravatarHash', users.bio
FROM boards
INNER JOIN custom_domains ON custom_domains.board_id = boards.id
INNER JOIN users ON users.id = boards.user_id
WHERE custom_domains.domain = $1`,
			identifier)
	}
	if err != nil {
		log.Print(err)
		http.Error(w, err.Error(), 500)
		return BaseData{error: err}
	}

	// lists for <nav>
	var lists []List
	err = db.Select(&lists, `
SELECT id, name, slug
FROM lists
WHERE visible = true AND board_id = $1
ORDER BY pos
    `, board.Id)
	if err != nil {
		log.Print(err)
		http.Error(w, err.Error(), 500)
		return BaseData{error: err}
	}

	// prefs
	var jsonPrefs types.JsonText
	err = db.Get(&jsonPrefs, "SELECT preferences($1)", identifier)
	if err != nil {
		log.Print(err)
		http.Error(w, err.Error(), 500)
		return BaseData{error: err}
	}
	var prefs Preferences
	err = jsonPrefs.Unmarshal(&prefs)
	if err != nil {
		log.Print(err)
		http.Error(w, err.Error(), 500)
		return BaseData{error: err}
	}

	// pagination
	page := 1
	if val, ok := mux.Vars(r)["page"]; ok {
		page, err = strconv.Atoi(val)
		if err != nil {
			log.Print(err)
			http.Error(w, err.Error(), 400)
			return BaseData{error: err}
		}
	}
	hasPrev := false
	if page > 1 {
		hasPrev = true
	}

	return BaseData{
		Settings: settings,
		Board:    board,
		Author:   author,
		Lists:    lists,
		Prefs:    prefs,
		Page:     page,
		HasPrev:  hasPrev,
	}
}

func index(w http.ResponseWriter, r *http.Request) {
	context := getBaseData(w, r)
	if context.error != nil {
		return
	}

	context.Page = 1
	if val, ok := mux.Vars(r)["page"]; ok {
		page, err := strconv.Atoi(val)
		if err != nil {
			log.Print(err)
			http.Error(w, err.Error(), 400)
			return
		}
		context.Page = page
	}
	if context.Page > 1 {
		context.HasPrev = true
	} else {
		context.HasPrev = false
	}

	ppp := context.Prefs.PostsPerPage()

	// fetch home cards for home
	var cards []Card
	err := db.Select(&cards, `
SELECT cards.slug,
       cards.name,
       coalesce(cards.cover, '') as cover,
       cards.created_on,
       due,
       list_id
FROM cards
INNER JOIN lists ON lists.id = cards.list_id
WHERE lists.board_id = $1
  AND lists.visible = true
  AND cards.visible = true
ORDER BY cards.due DESC, cards.created_on DESC
OFFSET $2
LIMIT $3
    `, context.Board.Id, ppp*(context.Page-1), ppp+1)
	if err != nil {
		log.Print(err)
		http.Error(w, err.Error(), 500)
		return
	}

	if len(cards) > ppp {
		context.HasNext = true
		cards = cards[:ppp]
	} else {
		context.HasNext = false
	}

	context.Cards = cards

	fmt.Fprint(w,
		mustache.RenderFileInLayout("templates/list.html",
			"templates/base.html",
			context),
	)
}

func list(w http.ResponseWriter, r *http.Request) {
	context := getBaseData(w, r)
	if context.error != nil {
		return
	}

	ppp := context.Prefs.PostsPerPage()
	listSlug := mux.Vars(r)["list-slug"]

	// fetch home cards for this list
	var cards []Card
	err := db.Select(&cards, `
(
  SELECT slug,
         name,
         null AS due,
         created_on,
         0 AS pos,
         '' AS cover
  FROM lists
  WHERE board_id = $1
    AND slug = $2
    AND visible
) UNION ALL (
  SELECT cards.slug,
         cards.name,
         cards.due,
         cards.created_on,
         cards.pos,
         coalesce(cards.cover, '') AS cover
  FROM cards
  INNER JOIN lists
  ON lists.id = cards.list_id
  WHERE lists.slug = $2
    AND cards.visible
  OFFSET $3
  LIMIT $4
)
ORDER BY pos
    `, context.Board.Id, listSlug, ppp*(context.Page-1), ppp+1)
	if err != nil {
		log.Print(err)
		http.Error(w, err.Error(), 500)
		return
	}

	// the first row is a List dressed as a Card
	list := List{
		Name: cards[0].Name,
		Slug: cards[0].Slug,
	}
	cards = cards[1:]

	if len(cards) > ppp {
		context.HasNext = true
		cards = cards[:ppp]
	} else {
		context.HasNext = false
	}

	context.List = list
	context.Cards = cards

	fmt.Fprint(w,
		mustache.RenderFileInLayout("templates/list.html",
			"templates/base.html",
			context),
	)
}

func cardRedirect(w http.ResponseWriter, r *http.Request) {
	// from_list/list-id/card-id/
	vars := mux.Vars(r)
	listId := vars["list-id"]
	cardSlug := vars["card-slug"]

	// get list slug
	var listSlug string
	err := db.Get(&listSlug, "SELECT slug FROM lists WHERE id = $1", listId)
	if err != nil {
		log.Print(err)
		http.Error(w, err.Error(), 500)
		return
	}

	http.Redirect(w, r, "/"+listSlug+"/"+cardSlug+"/", 301)
}

func card(w http.ResponseWriter, r *http.Request) {
	context := getBaseData(w, r)
	if context.error != nil {
		return
	}

	vars := mux.Vars(r)
	listSlug := vars["list-slug"]
	cardSlug := vars["card-slug"]

	// fetch home cards for this list
	var cards []Card
	err := db.Select(&cards, `
SELECT slug, name, due, created_on, "desc", attachments, checklists, cover
FROM (
  (
    SELECT slug,
           name,
           null AS due,
           created_on,
           '' AS "desc",
           '""'::jsonb AS attachments,
           '""'::jsonb AS checklists,
           0 AS sort,
           '' AS cover
    FROM lists
    WHERE board_id = $1
      AND slug = $2
      AND visible
  ) UNION ALL (
    SELECT cards.slug,
           cards.name,
           cards.due,
           cards.created_on,
           cards.desc,
           cards.attachments,
           cards.checklists,
           1 AS sort,
           coalesce(cards.cover, '') AS cover
    FROM cards
    INNER JOIN lists
    ON lists.id = cards.list_id
    WHERE cards.slug = $3
      AND lists.slug = $2
      AND cards.visible
  )
) AS u
ORDER BY sort
	`, context.Board.Id, listSlug, cardSlug)
	if err != nil {
		log.Print(err)
		http.Error(w, err.Error(), 500)
		return
	}

	// the first row is a List dressed as a Card
	list := List{
		Name: cards[0].Name,
		Slug: cards[0].Slug,
	}
	context.List = list
	context.Card = cards[1]

	fmt.Fprint(w,
		mustache.RenderFileInLayout("templates/card.html",
			"templates/base.html",
			context),
	)
}

func main() {
	settings = LoadSettings()

	db, _ = sqlx.Connect("postgres", settings.DatabaseURL)
	db = db.Unsafe()

	router := mux.NewRouter()
	router.StrictSlash(true) // redirects '/path' to '/path/'

	router.HandleFunc("/from_list/{list-id}/{card-slug}/", cardRedirect)
	router.HandleFunc("/{list-slug}/{card-slug}/", card)
	router.HandleFunc("/{list-slug}/p/{page:[0-9]+}/", list)
	router.HandleFunc("/{list-slug}/", list)
	router.HandleFunc("/p/{page:[0-9]+}/", index)
	router.HandleFunc("/", index)

	http.Handle("/", router)

	port := os.Getenv("PORT")
	if port == "" {
		port = "5000"
	}
	log.Print("listening...")
	http.ListenAndServe(":"+port, nil)
}
