package main

import (
	"fmt"
	"github.com/MindscapeHQ/raygun4go"
	"github.com/carbocation/interpose"
	"github.com/carbocation/interpose/adaptors"
	"github.com/gorilla/feeds"
	"github.com/gorilla/mux"
	"github.com/hoisie/redis"
	"github.com/jmoiron/sqlx"
	"github.com/rs/cors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	_ "github.com/lib/pq"
)

var db *sqlx.DB
var rds redis.Client
var settings Settings

func index(w http.ResponseWriter, r *http.Request) {
	requestData := loadRequestData(r)
	countPageViews(requestData)
	// raygun error reporting
	raygun, err := raygun4go.New("trellocms", settings.RaygunAPIKey)
	if err != nil {
		log.Print("unable to create Raygun client: ", err.Error())
	}
	raygun.Request(r)
	defer raygun.HandleError()
	// ~

	// pagination
	if val, ok := mux.Vars(r)["page"]; ok {
		page, err := strconv.Atoi(val)
		if err != nil || page < 0 {
			log.Print(err.Error())
			log.Print(val + " is not a page number.")
			raygun.CreateError(err.Error())
			page = 1
		}
		requestData.Page = page
		if requestData.Page > 1 {
			requestData.HasPrev = true
		}
	}
	// ~

	// fetch cards for home
	err = completeWithIndexCards(&requestData)
	if err != nil {
		raygun.CreateError(err.Error())
		http.Error(w, err.Error(), 500)
		return
	}

	fmt.Fprint(w,
		renderOnTopOf(requestData,
			"templates/list.html",
			"templates/base.html",
		),
	)
}

func feed(w http.ResponseWriter, r *http.Request) {
	requestData := loadRequestData(r)
	// fetch cards for home
	err := completeWithIndexCards(&requestData)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// generate feed
	feed := &feeds.Feed{
		Title:       requestData.Board.Name,
		Link:        &feeds.Link{Href: requestData.BaseURL.String()},
		Description: requestData.Board.Desc,
		Author:      &feeds.Author{requestData.Board.User_id, ""},
		Created:     requestData.Cards[0].Date(),
	}
	feed.Items = []*feeds.Item{}
	for _, card := range requestData.Cards {
		feed.Items = append(feed.Items, &feeds.Item{
			Id:          card.Id,
			Title:       card.Name,
			Link:        &feeds.Link{Href: requestData.BaseURL.String() + "/c/" + card.Id},
			Description: card.Desc,
			Created:     card.Date(),
		})
	}
	rss, err := feed.ToRss()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	fmt.Fprint(w, rss)
}

func list(w http.ResponseWriter, r *http.Request) {
	requestData := loadRequestData(r)
	countPageViews(requestData)
	// raygun error reporting
	raygun, err := raygun4go.New("trellocms", settings.RaygunAPIKey)
	if err != nil {
		log.Print("unable to create Raygun client: ", err.Error())
	}
	raygun.Request(r)
	defer raygun.HandleError()
	// ~

	// pagination
	requestData.Page = 1
	requestData.HasPrev = false
	if val, ok := mux.Vars(r)["page"]; ok {
		page, err := strconv.Atoi(val)
		if err != nil || page < 0 {
			log.Print(err.Error())
			raygun.CreateError(err.Error())
			page = 1
		}
		requestData.Page = page
		if requestData.Page > 1 {
			requestData.HasPrev = true
		}
	}
	// ~

	ppp := requestData.Prefs.PostsPerPage()
	listSlug := mux.Vars(r)["list-slug"]

	// fetch home cards for this list
	var cards []Card
	err = db.Select(&cards, `
(
  SELECT slug,
         name,
         null AS due,
         id,
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
         cards.id,
         cards.pos,
         coalesce(cards.cover, '') AS cover
  FROM cards
  INNER JOIN lists
  ON lists.id = cards.list_id
  WHERE board_id = $1
    AND lists.slug = $2
    AND lists.visible
    AND cards.visible
  ORDER BY pos
  OFFSET $3
  LIMIT $4
)
ORDER BY pos
    `, requestData.Board.Id, listSlug, ppp*(requestData.Page-1), ppp+1)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			// don't report to raygun, we already know the error and it doesn't matter
			log.Print("list not found.")
			error404(w, r)
			return
		} else {
			log.Print(err.Error())
			raygun.CreateError(err.Error())
			http.Error(w, "An unknown error has ocurred, we are sorry.", 500)
			return
		}
	}

	// we haven't found the requested list (when the list has 0 cards, we should have 1 here)
	if len(cards) < 1 {
		error404(w, r)
		return
	}

	// the first row is a List dressed as a Card
	list := List{
		Name: cards[0].Name,
		Slug: cards[0].Slug,
	}
	cards = cards[1:]

	if len(cards) > ppp {
		requestData.HasNext = true
		cards = cards[:ppp]
	} else {
		requestData.HasNext = false
	}

	requestData.Aggregator = list
	requestData.Cards = cards

	fmt.Fprint(w,
		renderOnTopOf(requestData,
			"templates/list.html",
			"templates/base.html",
		),
	)
}

func label(w http.ResponseWriter, r *http.Request) {
	requestData := loadRequestData(r)
	countPageViews(requestData)
	// raygun error reporting
	raygun, err := raygun4go.New("trellocms", settings.RaygunAPIKey)
	if err != nil {
		log.Print("unable to create Raygun client: ", err.Error())
	}
	raygun.Request(r)
	defer raygun.HandleError()
	// ~

	// pagination
	requestData.Page = 1
	requestData.HasPrev = false
	if val, ok := mux.Vars(r)["page"]; ok {
		page, err := strconv.Atoi(val)
		if err != nil || page < 0 {
			log.Print(err.Error())
			raygun.CreateError(err.Error())
			page = 1
		}
		requestData.Page = page
		if requestData.Page > 1 {
			requestData.HasPrev = true
		}
	}
	// ~

	ppp := requestData.Prefs.PostsPerPage()
	labelSlug := mux.Vars(r)["label-slug"]

	// fetch home cards for this label
	var cards []Card
	err = db.Select(&cards, `
(
  SELECT slug,
         name,
         null AS due,
         id,
         0 AS pos,
         '' AS cover
  FROM labels
  WHERE board_id = $1
    AND slug = $2
) UNION ALL (
  SELECT cards.slug,
         cards.name,
         cards.due,
         cards.id,
         cards.pos,
         coalesce(cards.cover, '') AS cover
  FROM cards
  INNER JOIN labels
  ON labels.id = ANY(cards.labels)
  WHERE board_id = $1
    AND (labels.slug = $2 OR labels.id = $2)
    AND cards.visible
  ORDER BY pos
  OFFSET $3
  LIMIT $4
)
ORDER BY pos
    `, requestData.Board.Id, labelSlug, ppp*(requestData.Page-1), ppp+1)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			// don't report to raygun, we already know the error and it doesn't matter
			log.Print("label not found.")
			error404(w, r)
			return
		} else {
			log.Print(err.Error())
			raygun.CreateError(err.Error())
			http.Error(w, "An unknown error has ocurred, we are sorry.", 500)
			return
		}
	}

	// the first row is a Label dressed as a Card
	label := Label{
		Name:  cards[0].Name,
		Color: cards[0].Color,
		Slug:  cards[0].Slug,
	}
	cards = cards[1:]

	if len(cards) > ppp {
		requestData.HasNext = true
		cards = cards[:ppp]
	} else {
		requestData.HasNext = false
	}

	requestData.Aggregator = label
	requestData.Cards = cards

	fmt.Fprint(w,
		renderOnTopOf(requestData,
			"templates/list.html",
			"templates/base.html",
		),
	)
}

func cardRedirect(w http.ResponseWriter, r *http.Request) {
	identifier := mux.Vars(r)["card-id-or-shortLink"]
	kind := "id"
	if len(identifier) < 15 {
		kind = "shortLink"
	}

	var slugs []string
	err := db.Select(&slugs, fmt.Sprintf(`
WITH card AS (
  SELECT name, list_id, slug, visible
  FROM cards
  WHERE "%s" = $1
)
SELECT slug FROM (
  (SELECT
    CASE WHEN "pagesList" THEN '' ELSE lists.slug END AS slug,
    0 AS listfirst
  FROM lists
  INNER JOIN card ON list_id = lists.id
  WHERE lists.visible OR "pagesList")
UNION
  (SELECT
    CASE WHEN visible THEN slug ELSE name END AS slug,
    1 AS listfirst
  FROM card)
)y
ORDER BY listfirst
    `, kind), identifier)
	if err != nil || len(slugs) != 2 {
		// redirect to the actual Trello card instead
		http.Redirect(w, r, "https://trello.com/c/"+identifier, 302)
		return
	}
	http.Redirect(w, r, "/"+slugs[0]+"/"+slugs[1]+"/", 302)
}

func listRedirect(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["list-id"]

	var slug string
	err := db.Get(&slug, `
SELECT slug
FROM lists
WHERE id = $1
    `, id)
	if err != nil {
		log.Print("list not found.")
		error404(w, r)
		return
	}

	http.Redirect(w, r, "/"+slug+"/", 302)
}

func card(w http.ResponseWriter, r *http.Request) {
	requestData := loadRequestData(r)
	countPageViews(requestData)
	// raygun error reporting
	raygun, err := raygun4go.New("trellocms", settings.RaygunAPIKey)
	if err != nil {
		log.Print("unable to create Raygun client: ", err.Error())
	}
	raygun.Request(r)
	defer raygun.HandleError()
	// ~

	vars := mux.Vars(r)
	listSlug := vars["list-slug"]
	cardSlug := vars["card-slug"]

	// fetch this card and its parent list
	var cards []Card
	err = db.Select(&cards, `
SELECT slug, name, due, id, "desc", attachments, checklists, labels, cover
FROM (
  (
    SELECT slug,
           name,
           null AS due,
           id,
           '' AS "desc",
           '""'::jsonb AS attachments,
           '""'::jsonb AS checklists,
           '""'::json AS labels,
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
           cards.id,
           cards.desc,
           cards.attachments,
           cards.checklists,
           array_to_json(array(SELECT row_to_json(l) FROM labels AS l WHERE l.id = ANY(cards.labels))) AS labels,
           1 AS sort,
           coalesce(cards.cover, '') AS cover
    FROM cards
    INNER JOIN lists
    ON lists.id = cards.list_id
    WHERE board_id = $1
      AND cards.slug = $3
      AND lists.slug = $2
      AND cards.visible
    GROUP BY cards.id
  )
) AS u
ORDER BY sort
	`, requestData.Board.Id, listSlug, cardSlug)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			// don't report to raygun, we already know the error and it doesn't matter
			log.Print("card not found.")
			error404(w, r)
			return
		} else {
			raygun.CreateError(err.Error())
			log.Print(err.Error())
			http.Error(w, "An unknown error has ocurred, we are sorry.", 500)
			return
		}
	}

	// we haven't found the requested list and card
	if len(cards) < 2 {
		error404(w, r)
		return
	}

	// the first row is a List dressed as a Card
	list := List{
		Name: cards[0].Name,
		Slug: cards[0].Slug,
	}
	requestData.Aggregator = list
	requestData.Card = cards[1]

	fmt.Fprint(w,
		renderOnTopOf(requestData,
			"templates/card.html",
			"templates/base.html",
		),
	)
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	requestData := loadRequestData(r)
	countPageViews(requestData)
	// raygun error reporting
	raygun, err := raygun4go.New("trellocms", settings.RaygunAPIKey)
	if err != nil {
		log.Print("unable to create Raygun client: ", err.Error())
	}
	raygun.Request(r)
	defer raygun.HandleError()
	// ~

	values := r.URL.Query()
	query := values.Get("query")

	requestData.SearchQuery = query
	requestData.TypedSearchQuery = query != ""
	requestData.SearchResults, _ = search(query, requestData.Board.Id)
	fmt.Fprint(w,
		renderOnTopOf(requestData,
			"templates/search.html",
			"templates/base.html",
		),
	)
}

func cardDesc(w http.ResponseWriter, r *http.Request) {
	identifier := mux.Vars(r)["card-id-or-shortLink"]
	kind := "id"
	if len(identifier) < 15 {
		kind = "shortLink"
	}
	qs, _ := url.ParseQuery(r.URL.RawQuery)
	var limit int
	var err error
	limit = 200
	if val, ok := qs["limit"]; ok {
		limit, err = strconv.Atoi(val[0])
		if err != nil {
			limit = 200
		}
	}

	var desc string
	err = db.Get(&desc, fmt.Sprintf(`
SELECT substring("desc" from 0 for $2)
FROM cards
WHERE "%s" = $1
    `, kind), identifier, limit)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			log.Print("there is not a card here.")
			error404(w, r)
			return
		} else {
			log.Print(err.Error())
			http.Error(w, "An unknown error has ocurred, we are sorry.", 500)
		}
		return
	}
	fmt.Fprint(w, desc)
}

func error404(w http.ResponseWriter, r *http.Request) {
	requestData := loadRequestData(r)
	w.WriteHeader(404)

	query := strings.Join(filter(strings.Split(r.URL.Path, "/"), func(s string) bool {
		if s == "" {
			return false
		}
		return true
	}), " ")
	query = strings.Join(strings.Split(query, "-"), " ")

	requestData.SearchQuery = query
	requestData.TypedSearchQuery = true
	requestData.SearchResults, _ = search(query, requestData.Board.Id)

	fmt.Fprint(w,
		renderOnTopOf(requestData,
			"templates/search.html",
			"templates/404.html",
			"templates/base.html",
		),
	)
}

func opensearch(w http.ResponseWriter, r *http.Request) {
	requestData := loadRequestData(r)
	fmt.Fprint(w, renderOnTopOf(requestData, "templates/opensearch.xml"))
}

func favicon(w http.ResponseWriter, r *http.Request) {
	requestData := loadRequestData(r)
	// raygun error reporting
	raygun, err := raygun4go.New("trellocms", settings.RaygunAPIKey)
	if err != nil {
		log.Print("unable to create Raygun client: ", err.Error())
	}
	raygun.Request(r)
	defer raygun.HandleError()
	// ~

	var fav string
	if requestData.Prefs.Favicon != "" {
		fav = requestData.Prefs.Favicon
	} else {
		fav = "http://lorempixel.com/32/32/"
	}
	http.Redirect(w, r, fav, 301)
}

func main() {
	settings = LoadSettings()

	db, _ = sqlx.Connect("postgres", settings.DatabaseURL)
	db = db.Unsafe()
	db.SetMaxOpenConns(7)

	rds.Addr = settings.RedisAddr
	rds.Password = settings.RedisPassword
	rds.MaxPoolSize = settings.RedisPoolSize

	CardLinkMatcher = regexp.MustCompile(CardLinkMatcherExpression)

	// middleware
	middle := interpose.New()
	middle.Use(clearContextMiddleware)
	middle.Use(adaptors.FromNegroni(cors.New(cors.Options{
		// CORS
		AllowedOrigins: []string{"*"},
	})))

	middle.Use(func(next http.Handler) http.Handler {
		// fetch requestData
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestData := getRequestData(w, r)
			// when there is an error, abort and return
			// (the http status and message should have been already set at getBaseData)
			if requestData.error != nil {
				return
			}
			saveRequestData(r, requestData)
			next.ServeHTTP(w, r)
		})
	})

	middle.Use(func(next http.Handler) http.Handler {
		// try to return a standalone page
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestData := loadRequestData(r)
			card, err := getPageAt(requestData, r.URL.Path)
			if err != nil {
				next.ServeHTTP(w, r)
			} else {
				countPageViews(requestData)
				requestData.Card = card
				fmt.Fprint(w,
					renderOnTopOf(requestData,
						"templates/card.html",
						"templates/base.html",
					),
				)
			}
		})
	})
	// ~

	// router
	router := mux.NewRouter()
	router.StrictSlash(true) // redirects '/path' to '/path/'
	middle.UseHandler(router)

	// > static
	router.HandleFunc("/favicon.ico", favicon)
	router.HandleFunc("/robots.txt", error404)
	router.HandleFunc("/opensearch.xml", opensearch)
	router.HandleFunc("/feed.xml", feed)

	// > redirect from permalinks
	router.HandleFunc("/c/{card-id-or-shortLink}/", cardRedirect)
	router.HandleFunc("/l/{list-id}/", listRedirect)

	// > helpers
	router.HandleFunc("/c/{card-id-or-shortLink}/desc", cardDesc)
	router.HandleFunc("/search/", handleSearch)

	// > normal pages and index
	router.HandleFunc("/p/{page:[0-9]+}/", index)
	router.HandleFunc("/tag/{label-slug}/", label)
	router.HandleFunc("/{list-slug}/p/{page:[0-9]+}/", list)
	router.HandleFunc("/{list-slug}/{card-slug}/", card)
	router.HandleFunc("/{list-slug}/", list)
	router.HandleFunc("/", index)

	// > errors
	router.NotFoundHandler = http.HandlerFunc(error404)
	// ~

	log.Print(":: SITES :: listening at port " + settings.Port)
	http.ListenAndServe(":"+settings.Port, middle)
}
