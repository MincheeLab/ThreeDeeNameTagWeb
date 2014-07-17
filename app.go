package main

import (
	"github.com/go-martini/martini"
	"github.com/martini-contrib/render"

	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"appengine"
	"appengine/blobstore"
	"appengine/datastore"
	"appengine/image"
	"appengine/mail"
	"appengine/user"
)

type Nametag struct {
	Id                int64
	Email             string
	Phone             string
	Content           string
	NormalizedContent string
	Status            string
	Images            []appengine.BlobKey

	CreatedAt time.Time
}

func (self *Nametag) IsPending() bool {
	return self.Status == "pending"
}

func (self *Nametag) IsPrinted() bool {
	return self.Status == "printed"
}

func (self *Nametag) IsNotified() bool {
	return self.Status == "notified"
}

func (self *Nametag) IsStatusEmpty() bool {
	return self.Status == ""
}

type NametagShowData struct {
	TagInfo     *Nametag
	UploadUrl   *url.URL
	Images      []Image
	RelatedTags []Nametag
}

type Image struct {
	BlobKey    appengine.BlobKey
	ServingURL *url.URL `datastore:"-"`
}

func init() {
	m := martini.Classic()
	m.Use(render.Renderer(render.Options{
		Layout: "layout",
		Funcs: []template.FuncMap{
			{
				"IsStringEquals": func(a string, b string) bool {
					return a == b
				},
			},
		},
	}))

	m.Get("/", Index)
	m.Get("/find", NametagsFind)
	m.Get("/show/:id", NametagsShowPublic)
	m.Get("/serve", Serve)

	m.Post("/nametags/:id/upload_image", authorize, NametagUploadImage)
	m.Get("/nametags/new", authorize, NametagsNew)
	m.Get("/nametags", authorize, NametagsList)

	m.Get("/nametags/send_email_to_all", authorize, SendEmailToAll)
	m.Get("/nametags/:id", authorize, NametagsShow)
	m.Get("/nametags/:id/mark_as", authorize, MarkAs)
	m.Get("/nametags/:id/notify", authorize, NametagNotify)

	m.Get("/nametags/:id/delete_confirmation", NametagDeleteConfirmation)
	m.Post("/nametags/:id/delete", NametagDelete)
	m.Post("/nametags/create", authorize, NametagsCreate)

	m.Get("/nametags/send_email_to_all_confirmation", authorize, func(r render.Render) {
		r.HTML(200, "nametags/send_email_to_all_confirmation", "")
	})

	m.Get("/pickup_location", func(r render.Render) {
		r.HTML(200, "pickup_location", "")
	})

	http.Handle("/", m)
}

func authorize(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	u := user.Current(c)
	if u == nil {
		url, err := user.LoginURL(c, r.URL.String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Location", url)
		w.WriteHeader(http.StatusFound)
		return
	}
}

func NametagsNew(r render.Render, res http.ResponseWriter, req *http.Request) {
	r.HTML(200, "nametags/new", "")
}

func NametagsFind(w http.ResponseWriter, req *http.Request) {
	c := appengine.NewContext(req)
	q := datastore.NewQuery("Nametag").Ancestor(getNametagCollectionKey(c)).
		Filter("Email =", req.FormValue("email")).
		Filter("NormalizedContent =", strings.ToLower(req.FormValue("content")))
	var nametags []Nametag
	keys, err := q.GetAll(c, &nametags)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(nametags) == 0 {
		http.Redirect(w, req, "/", http.StatusFound)
	}

	http.Redirect(w, req, fmt.Sprintf("/show/%v", strconv.FormatInt(keys[0].IntID(), 10)), http.StatusFound)

}

func NametagsList(r render.Render, w http.ResponseWriter, req *http.Request) {
	c := appengine.NewContext(req)
	_, models, err := findAllNametags(c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	r.HTML(200, "nametags/list", models)
}

func NametagsCreate(res http.ResponseWriter, req *http.Request) {
	c := appengine.NewContext(req)
	nametag := Nametag{
		Phone:             req.FormValue("phone"),
		Email:             req.FormValue("email"),
		Content:           req.FormValue("nametag_content"),
		NormalizedContent: strings.ToLower(req.FormValue("nametag_content")),
		CreatedAt:         time.Now(),
		Status:            "pending",
	}

	key := datastore.NewIncompleteKey(c, "Nametag", getNametagCollectionKey(c))
	if _, err := datastore.Put(c, key, &nametag); err != nil {
		http.Error(res, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(res, req, "/nametags", http.StatusFound)
}

func NametagsShow(r render.Render, params martini.Params, w http.ResponseWriter, req *http.Request) {
	c := appengine.NewContext(req)
	key, nametag := findOneNametagByParamId(c, w, params["id"])

	uploadURL, err := blobstore.UploadURL(c, fmt.Sprintf("/nametags/%s/upload_image", params["id"]), nil)
	if err != nil {
		c.Errorf("%v", err)
	}

	var images []Image
	q := datastore.NewQuery("Image").Ancestor(key)

	if _, err := q.GetAll(c, &images); err != nil {
		c.Errorf("%v", err)
	}

	options := &image.ServingURLOptions{
		Size: 500,
	}

	for i, img := range images {
		sUrl, _ := image.ServingURL(c, img.BlobKey, options)
		images[i].ServingURL = sUrl
	}

	data := NametagShowData{
		TagInfo:   nametag,
		UploadUrl: uploadURL,
		Images:    images,
	}

	r.HTML(200, "nametags/show", data)
}

func NametagsShowPublic(r render.Render, params martini.Params, w http.ResponseWriter, req *http.Request) {
	c := appengine.NewContext(req)
	key, nametag := findOneNametagByParamId(c, w, params["id"])

	var images []Image
	q := datastore.NewQuery("Image").Ancestor(key)

	if _, err := q.GetAll(c, &images); err != nil {
		c.Errorf("%v", err)
	}

	mtq := datastore.NewQuery("Nametag").Ancestor(getNametagCollectionKey(c)).
		Filter("Email =", nametag.Email)
	var nametags []Nametag
	keys, err := mtq.GetAll(c, &nametags)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	extraTags := make([]Nametag, len(nametags))
	for i := 0; i < len(nametags); i++ {
		if nametags[i].Content == nametag.Content {
			continue
		}
		extraTags[i].Id = keys[i].IntID()
		extraTags[i].Email = nametags[i].Email
		extraTags[i].Content = nametags[i].Content
		extraTags[i].CreatedAt = nametags[i].CreatedAt
		extraTags[i].Status = nametags[i].Status
	}

	options := &image.ServingURLOptions{
		Size: 500,
	}

	for i, img := range images {
		sUrl, _ := image.ServingURL(c, img.BlobKey, options)
		images[i].ServingURL = sUrl
	}

	data := NametagShowData{
		TagInfo:     nametag,
		Images:      images,
		RelatedTags: extraTags,
	}

	r.HTML(200, "nametags/show_public", data)
}

func NametagUploadImage(params martini.Params, w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	blobs, _, err := blobstore.ParseUpload(r)
	if err != nil {
		// serveError(c, w, err)
		return
	}
	file := blobs["file"]
	if len(file) == 0 {
		c.Errorf("no file uploaded")
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	intId, _ := strconv.ParseInt(params["id"], 0, 64)
	key := datastore.NewKey(c, "Nametag", "", intId, getNametagCollectionKey(c))
	imgKey := datastore.NewIncompleteKey(c, "Image", key)
	image := Image{
		BlobKey: file[0].BlobKey,
	}
	if _, err := datastore.Put(c, imgKey, &image); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// http.Redirect(w, r, "/serve?blobKey="+string(file[0].BlobKey), http.StatusFound)
	http.Redirect(w, r, "/nametags/"+params["id"], http.StatusFound)
}

func Serve(w http.ResponseWriter, r *http.Request) {
	blobstore.Send(w, appengine.BlobKey(r.FormValue("blobKey")))
}

func Index(r render.Render) {
	r.HTML(200, "index", "")
}

func getNametagCollectionKey(c appengine.Context) *datastore.Key {
	return datastore.NewKey(c, "NametagCollection", "nametag_collection", 0, nil)
}

const delayMsgText = `
您好，

歡迎你參加 「MincheeLab勉智實驗室 X USJ聖若瑟大學」 舉辦的的 「3D打印名字鑰匙扣」活動，由於3D打印機需要維護關係，展會後期的鑰匙扣打印有所延遲，敬請見諒。

現時，你可以到這個網站查詢你已提交的3D打印鑰匙扣的打印進度
http://nametag.minchee.org/

已完成打印鑰匙扣的將會另行電郵通知提取

勉智實驗室

Dear Sir/Madam,

We are excited that you joined the “3D Printed Name Keychain” event hosted by “MincheeLab X USJ”. Due to maintenance to the 3D printer, all requests that have been submitted for pick up after the Macau and Science Technology Week 2014 have been delayed.

Now you can check the status of your keychain printing on the following website: http://nametag.minchee.org/

For those in the keychain printing queue, another email will be sent to you once your keychain is ready for pickup.

MincheeLab

`

const pickupMsgText = `
您好，

歡迎你參加 MincheeLab勉智實驗室 X USJ聖若瑟大學 舉辦的的 "3D打印名牌" 活動。
你的名牌已打印完成。請到

澳門外港新填海區 16號
聖若瑟大學
電腦部

提取
請注意，必須要在電腦部

`

const sender = "MincheeLab勉智實驗室 <@example.com>"

func SendEmailToAll(r *http.Request, w http.ResponseWriter) {
	c := appengine.NewContext(r)
	_, models, err := findAllNametags(c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	var emails []string
	for i := 0; i < len(models); i++ {
		for _, email := range emails {
			if email == models[i].Email {
				continue
			}
		}

		emails = append(emails, models[i].Email)
	}

	msg := &mail.Message{
		Sender:  sender,
		To:      emails,
		Subject: "延遲通知－3D打印名字鑰匙扣",
		Body:    delayMsgText,
	}

	if err := mail.Send(c, msg); err != nil {
		c.Errorf("Couldn't send email: %v", err)
	}

	http.Redirect(w, r, "/nametags", http.StatusFound)
}

func NametagNotify(params martini.Params, w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	key, nametag := findOneNametagByParamId(c, w, params["id"])

	if r.FormValue("message_type") == "" {
		http.Error(w, "Missing message_type", http.StatusInternalServerError)
	}

	msg := &mail.Message{
		Sender:  sender,
		To:      []string{nametag.Email},
		Subject: "已完成－3D打印名字鑰匙扣",
		Body:    fmt.Sprintf(pickupMsgText, "http://nametag.minchee.org/show/"+strconv.FormatInt(key.IntID(), 10)),
	}

	if err := mail.Send(c, msg); err != nil {
		c.Errorf("Couldn't send email: %v", err)
	}

	UpdateStatus(c, key, nametag, "notified")
	http.Redirect(w, r, "/nametags", http.StatusFound)
}

func NametagDelete(params martini.Params, w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	key, _ := findOneNametagByParamId(c, w, params["id"])
	if err := datastore.Delete(c, key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/nametags", http.StatusFound)
}

func NametagDeleteConfirmation(r render.Render, params martini.Params, w http.ResponseWriter, req *http.Request) {
	c := appengine.NewContext(req)
	_, nametag := findOneNametagByParamId(c, w, params["id"])
	data := NametagShowData{
		TagInfo: nametag,
	}
	r.HTML(200, "nametags/delete_confirmation", data)
}

func MarkAs(params martini.Params, w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	key, nametag := findOneNametagByParamId(c, w, params["id"])

	if r.FormValue("status") == "" {
		http.Error(w, "No status defined", http.StatusInternalServerError)
	}

	UpdateStatus(c, key, nametag, r.FormValue("status"))
	http.Redirect(w, r, "/nametags/", http.StatusFound)
}

func UpdateStatus(c appengine.Context, key *datastore.Key, nametag *Nametag, status string) {
	nametag.Status = status
	datastore.Put(c, key, nametag)
}

func findOneNametagByParamId(c appengine.Context, w http.ResponseWriter, id string) (*datastore.Key, *Nametag) {
	intId, _ := strconv.ParseInt(id, 0, 64)
	key := datastore.NewKey(c, "Nametag", "", intId, getNametagCollectionKey(c))
	nametag := new(Nametag)
	if err := datastore.Get(c, key, nametag); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, nil
	}
	return key, nametag
}

func findAllNametags(c appengine.Context) ([]*datastore.Key, []Nametag, error) {
	q := datastore.NewQuery("Nametag").Ancestor(getNametagCollectionKey(c)).Order("-Email")
	var nametags []Nametag
	keys, err := q.GetAll(c, &nametags)
	if err != nil {
		return nil, nil, err
	}

	models := make([]Nametag, len(nametags))
	for i := 0; i < len(nametags); i++ {
		models[i].Id = keys[i].IntID()
		models[i].Email = nametags[i].Email
		models[i].Content = nametags[i].Content
		models[i].CreatedAt = nametags[i].CreatedAt
		models[i].Status = nametags[i].Status
	}

	return keys, models, err
}
