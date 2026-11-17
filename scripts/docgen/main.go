// Command docgen writes the website's docs pages in site/docs: the README,
// a few sections to a page, and a commands reference from wglink's own
// --help. CI runs it and fails if the committed pages differ, so the site
// always matches the README and the binary.
//
//	go -C scripts/docgen run .
package main

import (
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

const repo = "https://github.com/gureetk/wglink"

// A page is one docs page. Sections are README "## " headings, and "" is
// the text before the first one. The page's title stands in for its first
// section's heading; later sections keep theirs.
type page struct {
	Slug, Title, Group string
	Sections           []string
	Commands           bool   // the commands reference instead of README text
	Description        string // for search results; the first paragraph if empty
	Content            template.HTML
}

var pages = []*page{
	{Slug: "index", Title: "Overview", Group: "Getting started", Sections: []string{"", "Features"},
		Description: "Connect Linux machines over WireGuard from one inventory file. Plan, apply and check tunnels over SSH and Proxmox, with no agent."},
	{Slug: "install", Title: "Install", Group: "Getting started", Sections: []string{"Install"},
		Description: "Install wglink from a .deb or .rpm package, a release archive or Go, and what each machine it manages needs."},
	{Slug: "quick-start", Title: "Quick start", Group: "Getting started", Sections: []string{"Quick start"},
		Description: "Add three machines, connect them, and preview, apply and check the network."},
	{Slug: "inventory", Title: "The inventory", Group: "Getting started", Sections: []string{"The inventory"}},
	{Slug: "reaching-machines", Title: "Reaching machines", Group: "Guides", Sections: []string{"Reaching machines"},
		Description: "How wglink reaches nodes: SSH, the local machine, any exec command, and Proxmox containers and VMs."},
	{Slug: "phones", Title: "Phones and other devices", Group: "Guides", Sections: []string{"Phones and other devices"}},
	{Slug: "hub-and-spoke", Title: "Hub and spoke", Group: "Guides", Sections: []string{"Hub and spoke"}},
	{Slug: "routed-subnets", Title: "Routed subnets", Group: "Guides", Sections: []string{"Routed subnets"}},
	{Slug: "ipv6", Title: "IPv6", Group: "Guides", Sections: []string{"IPv6"}},
	{Slug: "preshared-keys", Title: "Preshared keys", Group: "Guides", Sections: []string{"Preshared keys"}},
	{Slug: "rotating-keys", Title: "Rotating keys", Group: "Guides", Sections: []string{"Rotating keys"}},
	{Slug: "removing", Title: "Removing nodes and networks", Group: "Guides", Sections: []string{"Removing nodes and networks"},
		Description: "Take nodes out of networks and remove networks; apply stops the interfaces that are no longer wanted and deletes their configs and keys."},
	{Slug: "dashboard", Title: "Dashboard", Group: "Guides", Sections: []string{"Dashboard"}},
	{Slug: "commands", Title: "Commands", Group: "Reference", Commands: true,
		Description: "Every wglink command, with the help it prints."},
	{Slug: "status", Title: "Status", Group: "Reference", Sections: []string{"Status"}},
	{Slug: "how-it-works", Title: "How it works", Group: "Reference", Sections: []string{"How it works"},
		Description: "What plan and apply do on your machines, step by step: probe, resolve, diff, apply and confirm."},
	{Slug: "security", Title: "Security", Group: "Reference", Sections: []string{"Security"},
		Description: "How wglink keeps private keys on their machines and secrets off the controller, and leaves your SSH setup and other configs alone."},
	{Slug: "compared", Title: "Compared with other tools", Group: "Reference", Sections: []string{"Compared with other tools"},
		Description: "How wglink compares with hand-written wg-quick configs, mesh VPN platforms and configuration management."},
	{Slug: "development", Title: "Development", Group: "Project", Sections: []string{"Development", "Roadmap", "License"},
		Description: "Testing, releasing and the layout of the wglink code."},
}

func main() {
	root := flag.String("root", "../..", "the repository's root")
	flag.Parse()
	log.SetFlags(0)
	if err := run(*root); err != nil {
		log.Fatal(err)
	}
}

func run(root string) error {
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		return err
	}
	sections := split(string(readme))
	used := map[string]bool{}
	for _, p := range pages {
		if p.Commands {
			if p.Content, err = commands(root); err != nil {
				return err
			}
			continue
		}
		var src strings.Builder
		for i, name := range p.Sections {
			body, ok := sections[name]
			if !ok {
				return fmt.Errorf("README.md has no section %q for %s", name, p.Slug)
			}
			used[name] = true
			if i > 0 {
				fmt.Fprintf(&src, "## %s\n\n", name)
			}
			src.WriteString(body)
		}
		var first string
		if p.Content, first, err = render(src.String()); err != nil {
			return err
		}
		if p.Description == "" {
			p.Description = first
		}
	}
	for name := range sections {
		if !used[name] {
			return fmt.Errorf("README.md section %q is on no docs page; add it to pages in scripts/docgen/main.go", name)
		}
	}
	return write(filepath.Join(root, "site", "docs"))
}

// write replaces the pages in dir, so pages no longer made go too.
func write(dir string) error {
	old, err := filepath.Glob(filepath.Join(dir, "*.html"))
	if err != nil {
		return err
	}
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i, p := range pages {
		data := struct {
			Page       *page
			Pages      []*page
			Prev, Next *page
		}{Page: p, Pages: pages}
		if i > 0 {
			data.Prev = pages[i-1]
		}
		if i+1 < len(pages) {
			data.Next = pages[i+1]
		}
		var b bytes.Buffer
		if err := layout.Execute(&b, data); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, p.Slug+".html"), b.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

var fence = regexp.MustCompile("^(```|~~~)")

// split cuts the README into its "## " sections, by heading; "" holds the
// text before the first, without the title line and badges. Headings
// inside code blocks don't count.
func split(readme string) map[string]string {
	sections := map[string]string{}
	name, inCode := "", false
	var cur strings.Builder
	for _, line := range strings.SplitAfter(readme, "\n") {
		if fence.MatchString(line) {
			inCode = !inCode
		}
		switch {
		case !inCode && strings.HasPrefix(line, "## "):
			sections[name] = cur.String()
			name = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			cur.Reset()
			continue
		case name == "" && (strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "[![")):
			continue
		}
		cur.WriteString(line)
	}
	sections[name] = cur.String()
	return sections
}

// headingID is the anchor GitHub gives a plain heading.
func headingID(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// anchors maps a link to a README section, such as "#status", to the
// section's place on the site.
var anchors = func() map[string]string {
	m := map[string]string{}
	for _, p := range pages {
		for i, name := range p.Sections {
			to := href(p)
			if i > 0 {
				to += "#" + headingID(name)
			}
			if name != "" {
				m["#"+headingID(name)] = to
			}
		}
	}
	return m
}()

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID(), parser.WithASTTransformers(util.Prioritized(links{}, 100))),
)

// render turns Markdown into HTML, and returns its first paragraph as plain
// text too.
func render(src string) (template.HTML, string, error) {
	source := []byte(src)
	doc := md.Parser().Parse(text.NewReader(source))
	var b bytes.Buffer
	if err := md.Renderer().Render(&b, source, doc); err != nil {
		return "", "", err
	}
	return template.HTML(b.String()), firstParagraph(doc, source), nil
}

// firstParagraph returns the document's first paragraph as one line of
// text, cut to the length of a search result's snippet.
func firstParagraph(doc ast.Node, source []byte) string {
	para := doc.FirstChild()
	for para != nil && para.Kind() != ast.KindParagraph {
		para = para.NextSibling()
	}
	if para == nil {
		return ""
	}
	var b strings.Builder
	_ = ast.Walk(para, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := n.(*ast.Text); ok && entering {
			b.Write(t.Segment.Value(source))
			if t.SoftLineBreak() || t.HardLineBreak() {
				b.WriteByte(' ')
			}
		}
		return ast.WalkContinue, nil
	})
	s := strings.Join(strings.Fields(b.String()), " ")
	if len(s) > 160 {
		// End at a sentence if one ends late enough, or else at a word.
		if cut := strings.LastIndex(s[:160], ". "); cut >= 60 {
			s = s[:cut+1]
		} else if cut := strings.LastIndex(s[:157], " "); cut > 0 {
			s = strings.TrimRight(s[:cut], ",;:") + "…"
		}
	}
	if strings.HasSuffix(s, ":") {
		s = strings.TrimSuffix(s, ":") + "."
	}
	return s
}

// links points links to files in the repository at GitHub, and links to
// README sections at the page that has them.
type links struct{}

func (links) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		l, ok := n.(*ast.Link)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		dest := string(l.Destination)
		switch {
		case strings.HasPrefix(dest, "#"):
			if to, ok := anchors[dest]; ok {
				l.Destination = []byte(to)
			}
		case !strings.Contains(dest, ":"):
			l.Destination = []byte(repo + "/blob/main/" + dest)
		}
		return ast.WalkContinue, nil
	})
}

// A group is a set of commands, as the root command's help lists them.
type group struct {
	Title    string
	Commands []command
}

// A command is one wglink command and its help.
type command struct {
	Path, Short, Help string
}

var (
	commandLine   = regexp.MustCompile(`^  ([a-z][a-z0-9-]*)\s{2,}(.+)$`)
	otherHeadings = map[string]bool{"Usage:": true, "Aliases:": true, "Examples:": true, "Flags:": true, "Global Flags:": true, "Additional help topics:": true}
)

// commands builds wglink and lists every command with its --help.
func commands(root string) (template.HTML, error) {
	dir, err := os.MkdirTemp("", "wglink-docs")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "wglink")
	build := exec.Command("go", "build", "-o", bin, "./cmd/wglink")
	build.Dir, build.Stderr = root, os.Stderr
	if err := build.Run(); err != nil {
		return "", fmt.Errorf("building wglink: %w", err)
	}
	var groups []*group
	var walk func(path []string, short string) error
	walk = func(path []string, short string) error {
		help, err := exec.Command(bin, append(path, "--help")...).Output()
		if err != nil {
			return fmt.Errorf("wglink %s --help: %w", strings.Join(path, " "), err)
		}
		if len(path) > 0 {
			g := groups[len(groups)-1]
			g.Commands = append(g.Commands, command{Path: "wglink " + strings.Join(path, " "), Short: short, Help: string(help)})
		}
		if len(path) > 0 && path[0] == "completion" {
			return nil
		}
		// Subcommands are listed under headings after Usage:, such as
		// "Available Commands:" or a group's title.
		usage, listing := false, false
		for _, line := range strings.Split(string(help), "\n") {
			if line != "" && !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":") {
				usage = usage || line == "Usage:"
				listing = usage && !otherHeadings[line]
				if listing && len(path) == 0 {
					title := strings.TrimSuffix(line, ":")
					if title == "Additional Commands" {
						title = "Other commands"
					}
					groups = append(groups, &group{Title: title})
				}
				continue
			}
			m := commandLine.FindStringSubmatch(line)
			if !listing || m == nil || m[1] == "help" {
				continue
			}
			if err := walk(append(append([]string{}, path...), m[1]), m[2]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(nil, ""); err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := commandsTmpl.Execute(&b, groups); err != nil {
		return "", err
	}
	return template.HTML(b.String()), nil
}

var funcs = template.FuncMap{
	"id":       headingID,
	"href":     href,
	"newGroup": func(pages []*page, i int) bool { return i == 0 || pages[i-1].Group != pages[i].Group },
	"endGroup": func(pages []*page, i int) bool { return i == len(pages)-1 || pages[i+1].Group != pages[i].Group },
	"notice": func() template.HTML {
		return "<!-- Made by scripts/docgen from README.md and wglink's help. Edit those, not this file. -->"
	},
}

func href(p *page) string {
	if p.Slug == "index" {
		return "./"
	}
	return p.Slug + ".html"
}

var commandsTmpl = template.Must(template.New("commands").Funcs(funcs).Parse(`<p>Every command, with the help it prints. Run <code>wglink COMMAND --help</code> for the same.</p>
{{range .}}<p class="toc-title">{{.Title}}</p>
<ul class="toc">{{range .Commands}}
<li><a href="#{{id .Path}}"><code>{{.Path}}</code></a><span>{{.Short}}</span></li>{{end}}
</ul>
{{end}}{{range .}}<h2 id="{{id .Title}}">{{.Title}}</h2>
{{range .Commands}}<h3 id="{{id .Path}}"><code>{{.Path}}</code></h3>
<pre>{{.Help}}</pre>
{{end}}{{end}}`))

var layout = template.Must(template.New("page").Funcs(funcs).Parse(`<!doctype html>
{{notice}}
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Page.Title}} · wglink docs</title>
{{with .Page.Description}}<meta name="description" content="{{.}}">
{{end}}<meta name="color-scheme" content="light dark">
<link rel="icon" href="../favicon.svg" type="image/svg+xml">
<script>try{var t=localStorage.getItem("theme");if(t==="light"||t==="dark")document.documentElement.dataset.theme=t}catch(e){}</script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&amp;family=JetBrains+Mono:wght@400;600&amp;family=Space+Grotesk:wght@500;600;700&amp;display=swap">
<link rel="stylesheet" href="../style.css">
<script src="../site.js" defer></script>
</head>
<body>

<header class="top docs-top">
<div class="wrap">
<a class="brand" href="../"><span class="dot" aria-hidden="true"></span>wglink <span class="sub">docs</span></a>
<nav class="nav" aria-label="Main">
<a class="button" href="https://github.com/gureetk/wglink">GitHub</a>
<button class="button theme-toggle" type="button" aria-label="Switch theme">
<svg class="moon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
<svg class="sun" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/></svg>
</button>
</nav>
</div>
</header>

<div class="wrap docs">
<nav class="sidebar" aria-label="Docs">
{{range $i, $p := .Pages}}{{if newGroup $.Pages $i}}<div>
<p>{{$p.Group}}</p>
{{end}}<a href="{{href $p}}"{{if eq $p.Slug $.Page.Slug}} aria-current="page"{{end}}>{{$p.Title}}</a>
{{if endGroup $.Pages $i}}</div>
{{end}}{{end}}</nav>

<main class="article">
<h1>{{.Page.Title}}</h1>
{{.Page.Content}}
<nav class="pager" aria-label="Previous and next pages">
{{with .Prev}}<a href="{{href .}}"><span>Previous</span>{{.Title}}</a>
{{end}}{{with .Next}}<a class="next" href="{{href .}}"><span>Next</span>{{.Title}}</a>
{{end}}</nav>
</main>
</div>

<footer class="foot">
<div class="wrap">
<span>wglink is MIT licensed. Built by <a href="https://github.com/gureetk">gureet</a>.</span>
<nav aria-label="Project">
<a href="https://github.com/gureetk/wglink">Source</a>
<a href="https://github.com/gureetk/wglink/releases">Releases</a>
<a href="https://github.com/gureetk/wglink/blob/main/CHANGELOG.md">Changelog</a>
</nav>
</div>
</footer>

</body>
</html>
`))
