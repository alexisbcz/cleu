package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/urfave/cli/v3"
)

var Read = &cli.Command{
	Name: "read",
	Action: func(ctx context.Context, c *cli.Command) error {
		username := os.Getenv("IMAP_USERNAME")
		password := os.Getenv("IMAP_PASSWORD")
		host := os.Getenv("IMAP_HOST")
		port := os.Getenv("IMAP_PORT")

		if username == "" || password == "" || host == "" || port == "" {
			return fmt.Errorf("please set IMAP_USERNAME, IMAP_PASSWORD, IMAP_HOST, and IMAP_PORT environment variables")
		}

		app := NewApp(username, password, host, port)
		p := tea.NewProgram(app, tea.WithAltScreen())
		_, err := p.Run()
		return err
	},
}

// Email represents an email message
type Email struct {
	UID         uint32
	Subject     string
	From        string
	To          string
	Date        time.Time
	Body        string
	HTMLBody    string
	TextBody    string
	ContentType string
	Seen        bool
}

func (e Email) FilterValue() string { return e.Subject }
func (e Email) Title() string {
	if len(e.Subject) > 60 {
		return e.Subject[:57] + "..."
	}
	return e.Subject
}
func (e Email) Description() string {
	status := "🔵"
	if e.Seen {
		status = "⚪"
	}
	return fmt.Sprintf("%s %s - %s", status, e.From, e.Date.Format("Jan 2, 15:04"))
}

// App represents the main application state
type App struct {
	username string
	password string
	host     string
	port     string

	client   *client.Client
	emails   []Email
	list     list.Model
	viewport viewport.Model
	ready    bool
	loading  bool
	err      error
	state    appState
}

type appState int

const (
	listView appState = iota
	emailView
)

// Messages for tea program
type emailsLoadedMsg []Email
type errorMsg error
type emailBodyLoadedMsg struct {
	uid  uint32
	body Email
}

func NewApp(username, password, host, port string) *App {
	// Initialize with empty list to prevent nil pointer issues
	delegate := list.NewDefaultDelegate()
	delegate.SetHeight(3) // Make items taller for better readability

	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "📧 Email Inbox (Loading...)"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetFilteringEnabled(true)

	return &App{
		username: username,
		password: password,
		host:     host,
		port:     port,
		list:     l,
		loading:  true,
		state:    listView,
	}
}

func (a *App) Init() tea.Cmd {
	return a.loadEmails()
}

func (a *App) loadEmails() tea.Cmd {
	return func() tea.Msg {
		client, err := connectToServer(a.username, a.password, a.host, a.port)
		if err != nil {
			return errorMsg(err)
		}
		a.client = client

		emails, err := fetchEmails(client)
		if err != nil {
			return errorMsg(err)
		}

		return emailsLoadedMsg(emails)
	}
}

func (a *App) loadEmailBody(uid uint32) tea.Cmd {
	return func() tea.Msg {
		email, err := fetchEmailBodyParsed(a.client, uid)
		if err != nil {
			return errorMsg(err)
		}
		return emailBodyLoadedMsg{uid: uid, body: email}
	}
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if !a.ready {
			a.list.SetSize(msg.Width, msg.Height-2)
			a.viewport = viewport.New(msg.Width-4, msg.Height-4)
			a.viewport.Style = emailViewStyle
			a.ready = true
		} else {
			a.list.SetSize(msg.Width, msg.Height-2)
			a.viewport.Width = msg.Width - 4
			a.viewport.Height = msg.Height - 4
		}

	case emailsLoadedMsg:
		a.loading = false
		a.emails = []Email(msg)
		a.list.Title = fmt.Sprintf("📧 Email Inbox (%d emails)", len(a.emails))

		items := make([]list.Item, len(a.emails))
		for i, email := range a.emails {
			items[i] = email
		}

		a.list.SetItems(items)

	case emailBodyLoadedMsg:
		// Update the email body in our slice
		for i, email := range a.emails {
			if email.UID == msg.uid {
				a.emails[i].Body = msg.body.Body
				a.emails[i].HTMLBody = msg.body.HTMLBody
				a.emails[i].TextBody = msg.body.TextBody
				a.emails[i].ContentType = msg.body.ContentType
				break
			}
		}

		// If we're viewing this email, update the viewport
		if a.state == emailView && len(a.emails) > 0 && a.list.Index() < len(a.emails) {
			selectedEmail := a.emails[a.list.Index()]
			if selectedEmail.UID == msg.uid {
				content := formatEmailForView(selectedEmail)
				a.viewport.SetContent(content)
			}
		}

	case errorMsg:
		a.err = msg
		a.loading = false

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			if a.client != nil {
				a.client.Logout()
			}
			return a, tea.Quit

		case "enter":
			if a.state == listView && len(a.emails) > 0 && a.list.Index() < len(a.emails) {
				selectedEmail := a.emails[a.list.Index()]
				a.state = emailView

				if selectedEmail.Body == "" {
					a.viewport.SetContent(formatEmailForView(selectedEmail))
					return a, a.loadEmailBody(selectedEmail.UID)
				} else {
					content := formatEmailForView(selectedEmail)
					a.viewport.SetContent(content)
				}
			}

		case "esc", "backspace":
			if a.state == emailView {
				a.state = listView
			}
		}
	}

	var cmd tea.Cmd
	if a.state == listView {
		a.list, cmd = a.list.Update(msg)
	} else {
		a.viewport, cmd = a.viewport.Update(msg)
	}

	return a, cmd
}

func (a *App) View() string {
	if a.err != nil {
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\nPress 'q' to quit", a.err))
	}

	if !a.ready {
		return loadingStyle.Render("Initializing...")
	}

	if a.loading {
		return loadingStyle.Render("Loading emails...\n\nPress 'q' to quit")
	}

	switch a.state {
	case listView:
		view := a.list.View()
		if len(a.emails) == 0 {
			view = emptyStyle.Render("No emails found.\n\nPress 'q' to quit")
		} else {
			view += "\n" + helpStyle.Render("↑/↓: navigate • enter: read • /: search • q: quit")
		}
		return view
	case emailView:
		return a.viewport.View() + "\n" + helpStyle.Render("↑/↓: scroll • esc: back • q: quit")
	}

	return ""
}

// Styles
var (
	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Padding(0, 1)

	loadingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205")).
			Bold(true).
			Padding(1, 2)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true).
			Padding(1, 2)

	emptyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Padding(1, 2)

	emailViewStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("238")).
			Padding(1, 2)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39"))

	subjectStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	fromStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("220"))

	dateStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("242"))

	bodyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))
)

// Clean up excessive whitespace and formatting
func cleanupWhitespace(text string) string {
	// Normalize line endings
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	// Remove trailing spaces from lines
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	text = strings.Join(lines, "\n")

	// Reduce multiple consecutive blank lines to maximum of 2
	for strings.Contains(text, "\n\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n\n", "\n\n\n")
	}

	// Remove leading/trailing whitespace
	text = strings.TrimSpace(text)

	return text
}

// Smart email fetching - load headers first, bodies on demand
func fetchEmails(imapClient *client.Client) ([]Email, error) {
	mailbox, err := imapClient.Select("INBOX", false)
	if err != nil {
		return nil, err
	}

	if mailbox.Messages == 0 {
		return []Email{}, nil
	}

	// Fetch most recent 50 emails (or all if less than 50)
	start := uint32(1)
	if mailbox.Messages > 50 {
		start = mailbox.Messages - 49
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddRange(start, mailbox.Messages)

	// Only fetch headers initially for performance
	items := []imap.FetchItem{
		imap.FetchEnvelope,
		imap.FetchFlags,
		imap.FetchUid,
	}

	messages := make(chan *imap.Message, 10)

	go func() {
		if err := imapClient.Fetch(seqSet, items, messages); err != nil {
			log.Printf("Error fetching messages: %v", err)
		}
	}()

	var emails []Email
	for msg := range messages {
		if msg.Envelope == nil {
			continue
		}

		from := "Unknown"
		if len(msg.Envelope.From) > 0 && msg.Envelope.From[0] != nil {
			if msg.Envelope.From[0].PersonalName != "" {
				from = msg.Envelope.From[0].PersonalName
			} else {
				from = msg.Envelope.From[0].MailboxName + "@" + msg.Envelope.From[0].HostName
			}
		}

		to := ""
		if len(msg.Envelope.To) > 0 && msg.Envelope.To[0] != nil {
			if msg.Envelope.To[0].PersonalName != "" {
				to = msg.Envelope.To[0].PersonalName
			} else {
				to = msg.Envelope.To[0].MailboxName + "@" + msg.Envelope.To[0].HostName
			}
		}

		seen := false
		for _, flag := range msg.Flags {
			if flag == imap.SeenFlag {
				seen = true
				break
			}
		}

		subject := msg.Envelope.Subject
		if subject == "" {
			subject = "(No Subject)"
		}

		emails = append(emails, Email{
			UID:     msg.Uid,
			Subject: subject,
			From:    from,
			To:      to,
			Date:    msg.Envelope.Date,
			Seen:    seen,
		})
	}

	// Sort by date (newest first)
	sort.Slice(emails, func(i, j int) bool {
		return emails[i].Date.After(emails[j].Date)
	})

	return emails, nil
}

// Fetch and parse email body with proper HTML handling
func fetchEmailBodyParsed(imapClient *client.Client, uid uint32) (Email, error) {
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{section.FetchItem()}

	messages := make(chan *imap.Message, 1)

	go func() {
		if err := imapClient.UidFetch(seqSet, items, messages); err != nil {
			log.Printf("Error fetching message body: %v", err)
		}
	}()

	var email Email
	for msg := range messages {
		for _, value := range msg.Body {
			if reader, ok := value.(io.Reader); ok {
				rawBody, err := io.ReadAll(reader)
				if err != nil {
					return email, err
				}

				// Parse the email
				parsedEmail, err := parseEmailBody(string(rawBody))
				if err != nil {
					// Fallback to raw body if parsing fails
					email.Body = string(rawBody)
					email.ContentType = "text/plain"
				} else {
					email = parsedEmail
				}
				return email, nil
			}
		}
	}

	return email, fmt.Errorf("could not load email body")
}

// Parse email body and extract HTML/Text parts
func parseEmailBody(rawBody string) (Email, error) {
	var email Email

	// Parse the email message
	msg, err := mail.ReadMessage(strings.NewReader(rawBody))
	if err != nil {
		return email, err
	}

	contentType := msg.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}

	email.ContentType = mediaType

	if strings.HasPrefix(mediaType, "multipart/") {
		// Handle multipart messages
		boundary := params["boundary"]
		reader := multipart.NewReader(msg.Body, boundary)

		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				continue
			}

			partBody, err := io.ReadAll(part)
			if err != nil {
				continue
			}

			partContentType := part.Header.Get("Content-Type")
			partMediaType, _, _ := mime.ParseMediaType(partContentType)

			switch {
			case strings.HasPrefix(partMediaType, "text/html"):
				email.HTMLBody = string(partBody)
			case strings.HasPrefix(partMediaType, "text/plain"):
				email.TextBody = string(partBody)
			}
		}
	} else {
		// Handle single part messages
		body, err := io.ReadAll(msg.Body)
		if err != nil {
			return email, err
		}

		if strings.HasPrefix(mediaType, "text/html") {
			email.HTMLBody = string(body)
		} else {
			email.TextBody = string(body)
		}
	}

	// Convert HTML to Markdown if we have HTML content
	if email.TextBody != "" {
		email.Body = email.TextBody
	} else {
		if email.HTMLBody != "" {
			email.Body = email.HTMLBody
		}
	}

	return email, nil
}

func formatEmailForView(email Email) string {
	var content strings.Builder

	// Header section
	content.WriteString(subjectStyle.Render("📧 ") + subjectStyle.Render(email.Subject) + "\n\n")

	content.WriteString(fromStyle.Render("From: ") + email.From + "\n")
	if email.To != "" {
		content.WriteString(fromStyle.Render("To: ") + email.To + "\n")
	}
	content.WriteString(dateStyle.Render("Date: ") + email.Date.Format("Monday, January 2, 2006 at 3:04 PM") + "\n\n")

	// Separator
	content.WriteString(strings.Repeat("─", 60) + "\n\n")

	// Body
	if email.Body != "" {
		// Clean up the body text
		body := strings.TrimSpace(email.Body)
		body = cleanupWhitespace(body)

		// Create glamour renderer with auto-style
		r, err := glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
			glamour.WithWordWrap(80),
		)

		if err != nil {
			content.WriteString(bodyStyle.Render(body))
		} else {
			rendered, err := r.Render(body)
			if err != nil {
				content.WriteString(bodyStyle.Render(body))
			} else {
				// Clean up glamour output to remove excessive spacing
				rendered = cleanupWhitespace(rendered)
				// Remove extra newlines around code blocks that glamour sometimes adds
				rendered = regexp.MustCompile(`\n{3,}\n`).ReplaceAllString(rendered, "\n\n```\n")
				rendered = regexp.MustCompile(`\n\n{3,}`).ReplaceAllString(rendered, "\n```\n\n")
				content.WriteString(rendered)
			}
		}
	} else {
		content.WriteString(loadingStyle.Render("Loading email content..."))
	}

	return content.String()
}

func connectToServer(username, password, host, port string) (*client.Client, error) {
	c, err := client.DialTLS(fmt.Sprintf("%s:%s", host, port), nil)
	if err != nil {
		return nil, err
	}

	if err := c.Login(username, password); err != nil {
		return nil, err
	}

	return c, nil
}
