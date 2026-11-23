package parse

import (
	"testing"
)

func TestHeadlineArgs(t *testing.T) {
	b := buildContrib(t, true)
	p := mustParse(t, b, `/api/kb/docs?select=id,body_headline,s:search_headline(document=title,max_words=10,min_words=2,start_sel="<em>",highlight_all=true),articles(id,search_headline(document=raw))&body=wfts(english).cat&and=(search.fts.dog)&articles.search=fts.x&articles.limit=2`)
	f := p.Select.Fields
	if h := f[1].Headline; h == nil || h.Document.Name != "body" || h.Options != "" || h.Filter == nil || h.Filter.Column.Name != "body" {
		t.Fatalf("body_headline = %+v", f[1].Headline)
	}
	if h := f[2].Headline; h == nil || h.Document.Name != "title" || h.Options != `MaxWords=10, MinWords=2, HighlightAll=true, StartSel="<em>"` ||
		h.Filter == nil || h.Filter.Column.Name != "search" {
		t.Fatalf("search_headline = %+v", f[2].Headline)
	}
	emb := p.Select.Embeds[0].Node.Fields[1].Headline
	if emb == nil || emb.Document.Name != "raw" || emb.Filter == nil || emb.Filter.Value == p.Select.Fields[2].Headline.Filter.Value {
		t.Fatalf("embedded headline = %+v", emb)
	}

	cases := map[string]string{
		"/api/kb/docs?select=body_headline":                                              CodeInvalidFilter,
		"/api/kb/docs?select=body_headline&or=(body.fts.a,id.eq.1)":                      CodeInvalidFilter,
		"/api/kb/docs?select=body_headline&body=not.fts.a":                               CodeInvalidFilter,
		"/api/kb/docs?select=body_headline&body=fts.a&and=(body.fts.b)":                  CodeInvalidFilter,
		"/api/kb/docs?select=id,articles(search_headline(document=title))&search=fts.a":  CodeInvalidFilter,
		"/api/kb/docs?select=search_headline&search=fts.a":                               CodeInvalidSelect,
		"/api/kb/docs?select=search_headline(document=id)&search=fts.a":                  CodeInvalidSelect,
		"/api/kb/docs?select=search_headline(document=nope)&search=fts.a":                CodeInvalidSelect,
		"/api/kb/docs?select=search_headline(title)&search=fts.a":                        CodeInvalidSelect,
		"/api/kb/docs?select=search_headline()&search=fts.a":                             CodeInvalidSelect,
		"/api/kb/docs?select=search_headline(document=title,document=body)&search=fts.a": CodeInvalidSelect,
		"/api/kb/docs?select=body_headline(document=title)&body=fts.a":                   CodeInvalidSelect,
		"/api/kb/docs?select=body_headline(max_words=5)&body=fts.a":                      CodeInvalidSelect,
		"/api/kb/docs?select=body_headline(max_fragments=11)&body=fts.a":                 CodeInvalidSelect,
		"/api/kb/docs?select=body_headline(highlight_all=1)&body=fts.a":                  CodeInvalidSelect,
		`/api/kb/docs?select=body_headline(stop_sel="a\"b")&body=fts.a`:                  CodeInvalidSelect,
		"/api/kb/docs?select=body_headline(colour=red)&body=fts.a":                       CodeUnknownArgument,
		"/api/kb/docs?select=body_headline::text&body=fts.a":                             CodeInvalidSelect,
		"/api/kb/docs?select=title_headline&title=fts.a":                                 CodeUnknownColumn,
		"/api/kb/docs?select=id&body_headline=eq.x":                                      CodeUnknownColumn,
		"/api/kb/articles?select=meta_headline&meta=fts.a":                               CodeUnknownColumn,
	}
	for u, code := range cases {
		_, err := doParse(t, b, req{url: u}, allOn)
		if err == nil {
			t.Errorf("%s: no error", u)
			continue
		}
		wantCode(t, err, code)
	}

	off := buildContrib(t, false)
	p = mustParse(t, off, "/api/kb/articles?select=id,meta_headline,raw_headline&meta=wfts.a&raw=fts(simple).b")
	if p.Select.Fields[1].Headline.Filter.Column.Name != "meta" || p.Select.Fields[2].Headline.Filter.Column.Name != "raw" {
		t.Fatalf("json headlines = %+v", p.Select.Fields)
	}
}
