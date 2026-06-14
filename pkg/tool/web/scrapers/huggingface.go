package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// HuggingFace API types.

type hfModel struct {
	ModelID     string   `json:"modelId"`
	PipelineTag string   `json:"pipeline_tag"`
	LibraryName string   `json:"library_name"`
	Tags        []string `json:"tags"`
	Downloads   int      `json:"downloads"`
	Likes       int      `json:"likes"`
	Private     bool     `json:"private"`
	Gated       any      `json:"gated"`
	CardData    *struct {
		License  string   `json:"license"`
		Language any      `json:"language"`
		Datasets []string `json:"datasets"`
		Metrics  []string `json:"metrics"`
	} `json:"cardData"`
}

type hfDataset struct {
	ID          string   `json:"id"`
	Tags        []string `json:"tags"`
	Downloads   int      `json:"downloads"`
	Likes       int      `json:"likes"`
	Private     bool     `json:"private"`
	Gated       any      `json:"gated"`
	Description string   `json:"description"`
	CardData    *struct {
		License        string   `json:"license"`
		Language       any      `json:"language"`
		TaskCategories []string `json:"task_categories"`
		SizeCategories []string `json:"size_categories"`
	} `json:"cardData"`
}

type hfSpace struct {
	ID       string   `json:"id"`
	Author   string   `json:"author"`
	Title    string   `json:"title"`
	SDK      string   `json:"sdk"`
	Tags     []string `json:"tags"`
	Likes    int      `json:"likes"`
	Private  bool     `json:"private"`
	CardData *struct {
		License string `json:"license"`
		SDK     string `json:"sdk"`
		AppFile string `json:"app_file"`
	} `json:"cardData"`
}

type hfUser struct {
	Fullname    string `json:"fullname"`
	User        string `json:"user"`
	NumModels   int    `json:"numModels"`
	NumDatasets int    `json:"numDatasets"`
	NumSpaces   int    `json:"numSpaces"`
	Orgs        []struct {
		Name string `json:"name"`
	} `json:"orgs"`
}

type hfKind int

const (
	hfKindModel hfKind = iota
	hfKindDataset
	hfKindSpace
	hfKindModelOrUser
)

type hfParsedURL struct {
	kind hfKind
	ID   string
}

func parseHuggingFaceURL(u *url.URL) *hfParsedURL {
	if u.Hostname() != "huggingface.co" {
		return nil
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })

	if len(parts) == 0 {
		return nil
	}

	// /datasets/{org}/{name}
	if parts[0] == "datasets" && len(parts) >= 2 {
		return &hfParsedURL{kind: hfKindDataset, ID: strings.Join(parts[1:], "/")}
	}

	// /spaces/{org}/{name}
	if parts[0] == "spaces" && len(parts) >= 3 {
		return &hfParsedURL{kind: hfKindSpace, ID: parts[1] + "/" + parts[2]}
	}

	// Skip non-resource paths.
	reserved := map[string]bool{
		"docs": true, "blog": true, "pricing": true,
		"enterprise": true, "join": true, "login": true, "settings": true,
	}
	if reserved[parts[0]] {
		return nil
	}

	// /{org}/{model}
	if len(parts) >= 2 {
		return &hfParsedURL{kind: hfKindModel, ID: parts[0] + "/" + parts[1]}
	}

	// /{id} — could be model or user
	if len(parts) == 1 {
		return &hfParsedURL{kind: hfKindModelOrUser, ID: parts[0]}
	}

	return nil
}

func HandleHuggingFace(ctx context.Context, u *url.URL, client *http.Client, _ JSFetcher) (*Result, error) {
	parsed := parseHuggingFaceURL(u)
	if parsed == nil {
		return nil, nil
	}

	switch parsed.kind {
	case hfKindModel:
		return handleHFModel(ctx, client, parsed.ID)
	case hfKindDataset:
		return handleHFDataset(ctx, client, parsed.ID)
	case hfKindSpace:
		return handleHFSpace(ctx, client, parsed.ID)
	case hfKindModelOrUser:
		return handleHFModelOrUser(ctx, client, parsed.ID)
	default:
		return nil, nil
	}
}

func handleHFModel(ctx context.Context, client *http.Client, id string) (*Result, error) {
	apiURL := "https://huggingface.co/api/models/" + id
	readmeURL := "https://huggingface.co/" + id + "/raw/main/README.md"

	var model hfModel
	if err := fetchAndDecode(ctx, client, apiURL, &model); err != nil {
		return nil, nil
	}

	md := renderHFModel(model)
	readme, _ := fetchPlainText(ctx, client, readmeURL)
	if readme != "" {
		md += "## Model Card\n\n" + readme
	}

	return &Result{Content: md, ContentType: "text/markdown", Method: "huggingface-api",
		Notes: []string{"Fetched via HuggingFace API"}}, nil
}

func handleHFDataset(ctx context.Context, client *http.Client, id string) (*Result, error) {
	apiURL := "https://huggingface.co/api/datasets/" + id
	readmeURL := "https://huggingface.co/datasets/" + id + "/raw/main/README.md"

	var dataset hfDataset
	if err := fetchAndDecode(ctx, client, apiURL, &dataset); err != nil {
		return nil, nil
	}

	md := renderHFDataset(dataset)
	readme, _ := fetchPlainText(ctx, client, readmeURL)
	if readme != "" {
		md += "## Dataset Card\n\n" + readme
	}

	return &Result{Content: md, ContentType: "text/markdown", Method: "huggingface-api",
		Notes: []string{"Fetched via HuggingFace API"}}, nil
}

func handleHFSpace(ctx context.Context, client *http.Client, id string) (*Result, error) {
	apiURL := "https://huggingface.co/api/spaces/" + id
	readmeURL := "https://huggingface.co/spaces/" + id + "/raw/main/README.md"

	var space hfSpace
	if err := fetchAndDecode(ctx, client, apiURL, &space); err != nil {
		return nil, nil
	}

	md := renderHFSpace(space)
	readme, _ := fetchPlainText(ctx, client, readmeURL)
	if readme != "" {
		md += "## Space Info\n\n" + readme
	}

	return &Result{Content: md, ContentType: "text/markdown", Method: "huggingface-api",
		Notes: []string{"Fetched via HuggingFace API"}}, nil
}

func handleHFModelOrUser(ctx context.Context, client *http.Client, id string) (*Result, error) {
	// Try model first.
	apiURL := "https://huggingface.co/api/models/" + id
	var model hfModel
	if err := fetchAndDecode(ctx, client, apiURL, &model); err == nil {
		md := renderHFModel(model)
		readme, _ := fetchPlainText(ctx, client, "https://huggingface.co/"+id+"/raw/main/README.md")
		if readme != "" {
			md += "## Model Card\n\n" + readme
		}
		return &Result{Content: md, ContentType: "text/markdown", Method: "huggingface-api",
			Notes: []string{"Fetched via HuggingFace API"}}, nil
	}

	// Fall back to user.
	userURL := "https://huggingface.co/api/users/" + id
	var user hfUser
	if err := fetchAndDecode(ctx, client, userURL, &user); err != nil {
		return nil, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", user.User)
	if user.Fullname != "" {
		fmt.Fprintf(&sb, "**Name:** %s\n", user.Fullname)
	}
	if user.NumModels > 0 {
		fmt.Fprintf(&sb, "**Models:** %s\n", formatNumber(user.NumModels))
	}
	if user.NumDatasets > 0 {
		fmt.Fprintf(&sb, "**Datasets:** %s\n", formatNumber(user.NumDatasets))
	}
	if user.NumSpaces > 0 {
		fmt.Fprintf(&sb, "**Spaces:** %s\n", formatNumber(user.NumSpaces))
	}
	if len(user.Orgs) > 0 {
		var names []string
		for _, o := range user.Orgs {
			names = append(names, o.Name)
		}
		fmt.Fprintf(&sb, "**Organizations:** %s\n", strings.Join(names, ", "))
	}

	return &Result{Content: sb.String(), ContentType: "text/markdown", Method: "huggingface-api",
		Notes: []string{"Fetched via HuggingFace API"}}, nil
}

func renderHFModel(m hfModel) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", m.ModelID)
	if m.PipelineTag != "" {
		fmt.Fprintf(&sb, "**Task:** %s\n", m.PipelineTag)
	}
	if m.LibraryName != "" {
		fmt.Fprintf(&sb, "**Library:** %s\n", m.LibraryName)
	}
	if m.Downloads > 0 {
		fmt.Fprintf(&sb, "**Downloads:** %s\n", formatNumber(m.Downloads))
	}
	if m.Likes > 0 {
		fmt.Fprintf(&sb, "**Likes:** %s\n", formatNumber(m.Likes))
	}
	if m.Private {
		sb.WriteString("**Visibility:** Private\n")
	}
	if m.Gated != nil && m.Gated != false {
		sb.WriteString("**Access:** Gated\n")
	}
	if m.CardData != nil {
		if m.CardData.License != "" {
			fmt.Fprintf(&sb, "**License:** %s\n", m.CardData.License)
		}
		renderLanguageField(&sb, m.CardData.Language)
		if len(m.CardData.Datasets) > 0 {
			fmt.Fprintf(&sb, "**Datasets:** %s\n", strings.Join(m.CardData.Datasets, ", "))
		}
		if len(m.CardData.Metrics) > 0 {
			fmt.Fprintf(&sb, "**Metrics:** %s\n", strings.Join(m.CardData.Metrics, ", "))
		}
	}
	if len(m.Tags) > 0 {
		fmt.Fprintf(&sb, "**Tags:** %s\n", strings.Join(m.Tags, ", "))
	}
	sb.WriteString("\n")
	return sb.String()
}

func renderHFDataset(d hfDataset) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", d.ID)
	if d.Description != "" {
		sb.WriteString(d.Description + "\n\n")
	}
	if d.Downloads > 0 {
		fmt.Fprintf(&sb, "**Downloads:** %s\n", formatNumber(d.Downloads))
	}
	if d.Likes > 0 {
		fmt.Fprintf(&sb, "**Likes:** %s\n", formatNumber(d.Likes))
	}
	if d.Private {
		sb.WriteString("**Visibility:** Private\n")
	}
	if d.Gated != nil && d.Gated != false {
		sb.WriteString("**Access:** Gated\n")
	}
	if d.CardData != nil {
		if d.CardData.License != "" {
			fmt.Fprintf(&sb, "**License:** %s\n", d.CardData.License)
		}
		renderLanguageField(&sb, d.CardData.Language)
		if len(d.CardData.TaskCategories) > 0 {
			fmt.Fprintf(&sb, "**Tasks:** %s\n", strings.Join(d.CardData.TaskCategories, ", "))
		}
		if len(d.CardData.SizeCategories) > 0 {
			fmt.Fprintf(&sb, "**Size:** %s\n", strings.Join(d.CardData.SizeCategories, ", "))
		}
	}
	if len(d.Tags) > 0 {
		fmt.Fprintf(&sb, "**Tags:** %s\n", strings.Join(d.Tags, ", "))
	}
	sb.WriteString("\n")
	return sb.String()
}

func renderHFSpace(s hfSpace) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", s.ID)
	if s.Title != "" {
		sb.WriteString(s.Title + "\n\n")
	}
	if s.Author != "" {
		fmt.Fprintf(&sb, "**Author:** %s\n", s.Author)
	}
	if s.SDK != "" {
		fmt.Fprintf(&sb, "**SDK:** %s\n", s.SDK)
	}
	if s.Likes > 0 {
		fmt.Fprintf(&sb, "**Likes:** %s\n", formatNumber(s.Likes))
	}
	if s.Private {
		sb.WriteString("**Visibility:** Private\n")
	}
	if s.CardData != nil {
		if s.CardData.License != "" {
			fmt.Fprintf(&sb, "**License:** %s\n", s.CardData.License)
		}
		if s.CardData.AppFile != "" {
			fmt.Fprintf(&sb, "**App File:** %s\n", s.CardData.AppFile)
		}
	}
	if len(s.Tags) > 0 {
		fmt.Fprintf(&sb, "**Tags:** %s\n", strings.Join(s.Tags, ", "))
	}
	sb.WriteString("\n")
	return sb.String()
}

// renderLanguageField handles language being either a string or []string.
func renderLanguageField(sb *strings.Builder, lang any) {
	if lang == nil {
		return
	}
	switch v := lang.(type) {
	case string:
		fmt.Fprintf(sb, "**Language:** %s\n", v)
	case []any:
		var parts []string
		for _, s := range v {
			parts = append(parts, fmt.Sprint(s))
		}
		fmt.Fprintf(sb, "**Language:** %s\n", strings.Join(parts, ", "))
	}
}

// fetchAndDecode fetches JSON and decodes into dst.
func fetchAndDecode(ctx context.Context, client *http.Client, apiURL string, dst any) error {
	raw, err := fetchJSON(ctx, client, apiURL)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// fetchPlainText fetches a URL and returns the body as string.
func fetchPlainText(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	data, err := fetchBytes(ctx, client, rawURL)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
