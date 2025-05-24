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

type LoadMoreItem struct{}

func (l LoadMoreItem) FilterValue() string { return "load more emails" }
func (l LoadMoreItem) Title() string       { return "📥 Load More Emails..." }
func (l LoadMoreItem) Description() string { return "Press Enter to load older emails" }

type App struct {
	username             string
	password             string
	host                 string
	port                 string
	client               *client.Client
	emails               []Email
	list                 list.Model
	viewport             viewport.Model
	ready                bool
	loading              bool
	loadingMore          bool
	err                  error
	state                appState
	totalMessages        uint32
	emailsPerPage        int
	currentPage          int
	hasMore              bool
	showDeleteConfirm    bool
	emailToDelete        *Email
	deleteConfirmIndex   int
	deletingEmail        bool
	deleteSuccess        bool
	deleteSuccessMessage string
}

type appState int

const (
	listView appState = iota
	emailView
	deleteConfirmView
)

type emailsLoadedMsg struct {
	emails        []Email
	totalMessages uint32
	isLoadMore    bool
}
type errorMsg error
type emailBodyLoadedMsg struct {
	uid  uint32
	body Email
}
type emailDeletedMsg struct {
	uid     uint32
	success bool
	message string
}

func NewApp(username, password, host, port string) *App {
	delegate := list.NewDefaultDelegate()
	delegate.SetHeight(3)
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "📧 Email Inbox (Loading...)"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)

	return &App{
		username:      username,
		password:      password,
		host:          host,
		port:          port,
		list:          l,
		loading:       true,
		state:         listView,
		emailsPerPage: 50,
		currentPage:   1,
	}
}

func (a *App) Init() tea.Cmd {
	return a.loadEmails(1, false)
}

func (a *App) loadEmails(page int, isLoadMore bool) tea.Cmd {
	return func() tea.Msg {
		if a.client == nil {
			client, err := connectToServer(a.username, a.password, a.host, a.port)
			if err != nil {
				return errorMsg(err)
			}
			a.client = client
		}

		emails, totalMessages, err := fetchEmails(a.client, page, a.emailsPerPage)
		if err != nil {
			return errorMsg(err)
		}

		return emailsLoadedMsg{
			emails:        emails,
			totalMessages: totalMessages,
			isLoadMore:    isLoadMore,
		}
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

func (a *App) deleteEmail(uid uint32) tea.Cmd {
	return func() tea.Msg {
		success, message := moveEmailToTrash(a.client, uid)
		a.deleteConfirmIndex = 0
		return emailDeletedMsg{
			uid:     uid,
			success: success,
			message: message,
		}
	}
}

func (a *App) updateEmailList() {
	items := make([]list.Item, len(a.emails))
	for i, email := range a.emails {
		items[i] = email
	}

	if a.hasMore {
		items = append(items, LoadMoreItem{})
	}

	a.list.SetItems(items)
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
		a.loadingMore = false
		a.totalMessages = msg.totalMessages

		if msg.isLoadMore {
			a.emails = append(a.emails, msg.emails...)
		} else {
			a.emails = msg.emails
		}

		loadedCount := len(a.emails)
		a.hasMore = uint32(loadedCount) < a.totalMessages

		title := fmt.Sprintf("📧 Email Inbox (%d of %d emails)", loadedCount, a.totalMessages)
		if a.hasMore {
			title += " • More available"
		}
		a.list.Title = title

		a.updateEmailList()

	case emailBodyLoadedMsg:
		for i, email := range a.emails {
			if email.UID == msg.uid {
				a.emails[i].Body = msg.body.Body
				a.emails[i].HTMLBody = msg.body.HTMLBody
				a.emails[i].TextBody = msg.body.TextBody
				a.emails[i].ContentType = msg.body.ContentType
				break
			}
		}
		if a.state == emailView && len(a.emails) > 0 && a.list.Index() < len(a.emails) {
			selectedEmail := a.emails[a.list.Index()]
			if selectedEmail.UID == msg.uid {
				content := formatEmailForView(selectedEmail)
				a.viewport.SetContent(content)
			}
		}

	case emailDeletedMsg:
		a.deletingEmail = false
		a.showDeleteConfirm = false
		a.state = listView

		if msg.success {
			for i, email := range a.emails {
				if email.UID == msg.uid {
					a.emails = append(a.emails[:i], a.emails[i+1:]...)
					break
				}
			}
			a.updateEmailList()

			a.totalMessages--
			title := fmt.Sprintf("📧 Email Inbox (%d of %d emails)", len(a.emails), a.totalMessages)
			if a.hasMore {
				title += " • More available"
			}
			a.list.Title = title

			a.deleteSuccess = true
			a.deleteSuccessMessage = msg.message

			return a, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearSuccessMsg{}
			})
		} else {
			a.err = fmt.Errorf("failed to delete email: %s", msg.message)
		}

	case clearSuccessMsg:
		a.deleteSuccess = false
		a.deleteSuccessMessage = ""

	case errorMsg:
		a.err = msg
		a.loading = false
		a.loadingMore = false
		a.deletingEmail = false

	case tea.KeyMsg:
		if a.state == deleteConfirmView {
			switch msg.String() {
			case "left", "h", "right", "l":
				if a.deleteConfirmIndex == 0 {
					a.deleteConfirmIndex = 1
				} else {
					a.deleteConfirmIndex = 0
				}
			case "enter":
				if a.deleteConfirmIndex == 1 && a.emailToDelete != nil {
					a.deletingEmail = true
					return a, a.deleteEmail(a.emailToDelete.UID)
				} else {
					a.showDeleteConfirm = false
					a.state = listView
					a.emailToDelete = nil
				}
			case "esc", "q":
				a.showDeleteConfirm = false
				a.state = listView
				a.emailToDelete = nil
			}
			return a, nil
		}

		switch msg.String() {
		case "ctrl+c", "q":
			if a.client != nil {
				a.client.Logout()
			}
			return a, tea.Quit

		case "enter":
			if a.state == listView && a.list.Index() < len(a.list.Items()) {
				selectedItem := a.list.SelectedItem()

				if _, isLoadMore := selectedItem.(LoadMoreItem); isLoadMore {
					if !a.loadingMore {
						a.loadingMore = true
						a.currentPage++
						return a, a.loadEmails(a.currentPage, true)
					}
					return a, nil
				}

				if a.list.Index() < len(a.emails) {
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
			}

		case "esc", "backspace":
			if a.state == emailView {
				a.state = listView
			}

		case "d":
			if (a.state == listView || a.state == emailView) && len(a.emails) > 0 {
				var emailToDelete *Email

				if a.state == emailView && a.list.Index() < len(a.emails) {
					emailToDelete = &a.emails[a.list.Index()]
				} else if a.state == listView && a.list.Index() < len(a.emails) {
					selectedItem := a.list.SelectedItem()
					if email, ok := selectedItem.(Email); ok {
						emailToDelete = &email
					}
				}

				if emailToDelete != nil {
					a.emailToDelete = emailToDelete
					a.showDeleteConfirm = true
					a.state = deleteConfirmView
				}
			}

		case "r":
			if a.state == listView && !a.loading {
				a.loading = true
				a.currentPage = 1
				a.list.Title = "📧 Email Inbox (Refreshing...)"
				return a, a.loadEmails(1, false)
			}
		}
	}

	var cmd tea.Cmd
	if a.state == listView {
		a.list, cmd = a.list.Update(msg)
	} else if a.state == emailView {
		a.viewport, cmd = a.viewport.Update(msg)
	}
	return a, cmd
}

type clearSuccessMsg struct{}

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

	if a.state == deleteConfirmView && a.emailToDelete != nil {
		return a.renderDeleteConfirmation()
	}

	switch a.state {
	case listView:
		view := a.list.View()
		if len(a.emails) == 0 {
			view = emptyStyle.Render("No emails found.\n\nPress 'q' to quit")
		} else {
			helpText := "↑/↓: navigate • enter: read • d: delete • /: search • r: refresh • q: quit"
			if a.loadingMore {
				helpText = "Loading more emails... • " + helpText
			}
			if a.deleteSuccess {
				successMsg := successStyle.Render("✓ " + a.deleteSuccessMessage)
				view += "\n" + successMsg
			}
			view += "\n" + helpStyle.Render(helpText)
		}
		return view

	case emailView:
		helpText := "↑/↓: scroll • d: delete • esc: back • q: quit"
		if a.deleteSuccess {
			successMsg := successStyle.Render("✓ " + a.deleteSuccessMessage)
			return a.viewport.View() + "\n" + successMsg + "\n" + helpStyle.Render(helpText)
		}
		return a.viewport.View() + "\n" + helpStyle.Render(helpText)
	}

	return ""
}

func (a *App) renderDeleteConfirmation() string {
	if a.deletingEmail {
		return loadingStyle.Render("Deleting email...\n\nPlease wait...")
	}

	var content strings.Builder

	content.WriteString(warningStyle.Render("🗑️  Delete Email") + "\n\n")
	content.WriteString("Are you sure you want to delete this email?\n\n")
	content.WriteString(emailInfoStyle.Render(fmt.Sprintf("Subject: %s", a.emailToDelete.Subject)) + "\n")
	content.WriteString(emailInfoStyle.Render(fmt.Sprintf("From: %s", a.emailToDelete.From)) + "\n")
	content.WriteString(emailInfoStyle.Render(fmt.Sprintf("Date: %s", a.emailToDelete.Date.Format("Jan 2, 2006 15:04"))) + "\n\n")

	content.WriteString("This will move the email to Trash.\n\n")

	noButton := "[ No ]"
	yesButton := "[ Yes ]"

	if a.deleteConfirmIndex == 0 {
		noButton = confirmButtonSelectedStyle.Render("[ No ]")
		yesButton = confirmButtonStyle.Render("[ Yes ]")
	} else {
		noButton = confirmButtonStyle.Render("[ No ]")
		yesButton = confirmButtonSelectedStyle.Render("[ Yes ]")
	}

	buttonsLine := lipgloss.JoinHorizontal(lipgloss.Center, noButton, "  ", yesButton)
	content.WriteString(buttonsLine + "\n\n")
	content.WriteString(helpStyle.Render("←/→: select • enter: confirm • esc: cancel"))

	return dialogStyle.Render(content.String())
}

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
	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("46")).
			Bold(true).
			Padding(0, 1)
	warningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("208")).
			Bold(true)
	dialogStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("208")).
			Padding(2, 4).
			MarginTop(2).
			MarginLeft(4)
	emailInfoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))
	confirmButtonStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("241")).
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("241")).
				Padding(0, 1)
	confirmButtonSelectedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("15")).
					Background(lipgloss.Color("196")).
					BorderStyle(lipgloss.RoundedBorder()).
					BorderForeground(lipgloss.Color("196")).
					Padding(0, 1).
					Bold(true)
)

func moveEmailToTrash(imapClient *client.Client, uid uint32) (bool, string) {
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	trashFolders := []string{"Trash", "INBOX.Trash", "Deleted Messages", "INBOX.Deleted Messages"}

	for _, trashFolder := range trashFolders {
		_, err := imapClient.Select(trashFolder, false)
		if err == nil {
			_, err = imapClient.Select("INBOX", false)
			if err != nil {
				continue
			}

			err = imapClient.UidMove(seqSet, trashFolder)
			if err == nil {
				return true, fmt.Sprintf("Email moved to %s", trashFolder)
			}
		}
	}

	_, err := imapClient.Select("INBOX", false)
	if err != nil {
		return false, fmt.Sprintf("Failed to select INBOX: %v", err)
	}

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.DeletedFlag}
	err = imapClient.UidStore(seqSet, item, flags, nil)
	if err != nil {
		return false, fmt.Sprintf("Failed to mark email as deleted: %v", err)
	}

	err = imapClient.Expunge(nil)
	if err != nil {
		return false, fmt.Sprintf("Failed to expunge: %v", err)
	}

	return true, "Email deleted permanently"
}

func cleanupWhitespace(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	text = strings.Join(lines, "\n")
	for strings.Contains(text, "\n\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n\n", "\n\n\n")
	}
	text = strings.TrimSpace(text)
	return text
}

func fetchEmails(imapClient *client.Client, page int, perPage int) ([]Email, uint32, error) {
	mailbox, err := imapClient.Select("INBOX", false)
	if err != nil {
		return nil, 0, err
	}

	if mailbox.Messages == 0 {
		return []Email{}, 0, nil
	}

	totalMessages := mailbox.Messages
	end := totalMessages - uint32((page-1)*perPage)
	start := end - uint32(perPage) + 1

	if start < 1 {
		start = 1
	}

	if end > totalMessages {
		end = totalMessages
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddRange(start, end)

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

	sort.Slice(emails, func(i, j int) bool {
		return emails[i].Date.After(emails[j].Date)
	})

	return emails, totalMessages, nil
}

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
				parsedEmail, err := parseEmailBody(string(rawBody))
				if err != nil {
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

func parseEmailBody(rawBody string) (Email, error) {
	var email Email
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
	content.WriteString(subjectStyle.Render("📧 ") + subjectStyle.Render(email.Subject) + "\n\n")
	content.WriteString(fromStyle.Render("From: ") + email.From + "\n")
	if email.To != "" {
		content.WriteString(fromStyle.Render("To: ") + email.To + "\n")
	}
	content.WriteString(dateStyle.Render("Date: ") + email.Date.Format("Monday, January 2, 2006 at 3:04 PM") + "\n\n")
	content.WriteString(strings.Repeat("─", 60) + "\n\n")
	if email.Body != "" {
		body := strings.TrimSpace(email.Body)
		body = cleanupWhitespace(body)
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
				rendered = cleanupWhitespace(rendered)
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
