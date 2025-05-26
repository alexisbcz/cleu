package cmd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/smtp"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/urfave/cli/v3"
)

var Send = &cli.Command{
	Name:  "send",
	Usage: "Send an email interactively",
	Action: func(ctx context.Context, c *cli.Command) error {
		// Get SMTP configuration from environment
		smtpHost := os.Getenv("SMTP_HOST")
		smtpPort := os.Getenv("SMTP_PORT")
		smtpUsername := os.Getenv("SMTP_USERNAME")
		smtpPassword := os.Getenv("SMTP_PASSWORD")
		fromEmail := os.Getenv("FROM_EMAIL")

		if smtpHost == "" || smtpPort == "" || smtpUsername == "" || smtpPassword == "" {
			return fmt.Errorf("please set SMTP_HOST, SMTP_PORT, SMTP_USERNAME, and SMTP_PASSWORD environment variables")
		}

		if fromEmail == "" {
			fromEmail = smtpUsername // Default to SMTP username if FROM_EMAIL not set
		}

		// Create the email form
		email := &EmailForm{}
		form := createEmailForm(email, fromEmail)

		// Run the form
		err := form.Run()
		if err != nil {
			return fmt.Errorf("form error: %w", err)
		}

		// Send the email
		return sendEmail(email, smtpHost, smtpPort, smtpUsername, smtpPassword)
	},
}

// EmailForm holds the form data
type EmailForm struct {
	To          string
	Cc          string
	Bcc         string
	Subject     string
	Body        string
	Priority    string
	Attachments string
	Confirm     bool
}

// createEmailForm creates the interactive form using huh
func createEmailForm(email *EmailForm, fromEmail string) *huh.Form {
	return huh.NewForm(
		// Basic email fields group
		huh.NewGroup(
			huh.NewInput().
				Title("To").
				Description("Recipient email address(es) - separate multiple with commas").
				Placeholder("recipient@example.com, another@example.com").
				Value(&email.To).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("recipient is required")
					}
					// Basic email validation for each recipient
					recipients := strings.Split(s, ",")
					for _, recipient := range recipients {
						recipient = strings.TrimSpace(recipient)
						if !strings.Contains(recipient, "@") || !strings.Contains(recipient, ".") {
							return fmt.Errorf("invalid email format: %s", recipient)
						}
					}
					return nil
				}),

			huh.NewInput().
				Title("Subject").
				Description("Email subject line").
				Placeholder("Enter subject").
				Value(&email.Subject).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("subject is required")
					}
					return nil
				}),
		),

		// Optional fields group
		huh.NewGroup(
			huh.NewInput().
				Title("Cc (Optional)").
				Description("Carbon copy recipients - separate multiple with commas").
				Placeholder("cc@example.com").
				Value(&email.Cc),

			huh.NewInput().
				Title("Bcc (Optional)").
				Description("Blind carbon copy recipients - separate multiple with commas").
				Placeholder("bcc@example.com").
				Value(&email.Bcc),

			huh.NewSelect[string]().
				Title("Priority").
				Description("Email priority level").
				Options(
					huh.NewOption("Normal", "normal"),
					huh.NewOption("High", "high"),
					huh.NewOption("Low", "low"),
				).
				Value(&email.Priority),
		),

		// Body group
		huh.NewGroup(
			huh.NewText().
				Title("Email Body").
				Description("Enter your email content (supports plain text and basic markdown)").
				Placeholder("Type your message here...").
				Value(&email.Body).
				Lines(10).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("email body is required")
					}
					return nil
				}),
		),

		// Confirmation group
		huh.NewGroup(
			huh.NewNote().
				Title("Email Summary").
				Description(fmt.Sprintf(
					"From: %s\nTo: %s\nSubject: %s\nPriority: %s",
					fromEmail,
					email.To,
					email.Subject,
					email.Priority,
				)),

			huh.NewConfirm().
				Title("Send Email").
				Description("Are you sure you want to send this email?").
				Value(&email.Confirm),
		),
	).WithTheme(huh.ThemeCharm())
}

// sendEmail sends the email using SMTP
func sendEmail(email *EmailForm, host, port, username, password string) error {
	if !email.Confirm {
		fmt.Println("Email sending cancelled.")
		return nil
	}

	// Parse recipients
	toRecipients := parseRecipients(email.To)
	ccRecipients := parseRecipients(email.Cc)
	bccRecipients := parseRecipients(email.Bcc)

	// Combine all recipients for SMTP
	allRecipients := append(toRecipients, ccRecipients...)
	allRecipients = append(allRecipients, bccRecipients...)

	if len(allRecipients) == 0 {
		return fmt.Errorf("no valid recipients found")
	}

	// Build the email message
	message := buildEmailMessage(email, username, toRecipients, ccRecipients)

	// Set up SMTP authentication
	auth := smtp.PlainAuth("", username, password, host)

	// Create TLS config
	tlsConfig := &tls.Config{
		InsecureSkipVerify: false,
		ServerName:         host,
	}

	// Connect to SMTP server
	serverAddr := fmt.Sprintf("%s:%s", host, port)

	// Try TLS connection first
	conn, err := tls.Dial("tcp", serverAddr, tlsConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to SMTP server: %w", err)
	}
	defer conn.Close()

	// Create SMTP client
	smtpClient, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("failed to create SMTP client: %w", err)
	}
	defer smtpClient.Quit()

	// Authenticate
	if err := smtpClient.Auth(auth); err != nil {
		return fmt.Errorf("SMTP authentication failed: %w", err)
	}

	// Set sender
	if err := smtpClient.Mail(username); err != nil {
		return fmt.Errorf("failed to set sender: %w", err)
	}

	// Set recipients
	for _, recipient := range allRecipients {
		if err := smtpClient.Rcpt(recipient); err != nil {
			return fmt.Errorf("failed to set recipient %s: %w", recipient, err)
		}
	}

	// Send message
	dataWriter, err := smtpClient.Data()
	if err != nil {
		return fmt.Errorf("failed to get data writer: %w", err)
	}

	_, err = dataWriter.Write([]byte(message))
	if err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	err = dataWriter.Close()
	if err != nil {
		return fmt.Errorf("failed to close data writer: %w", err)
	}

	fmt.Printf("✅ Email sent successfully to %d recipient(s)!\n", len(allRecipients))
	return nil
}

// parseRecipients parses comma-separated email addresses
func parseRecipients(recipients string) []string {
	if recipients == "" {
		return nil
	}

	var parsed []string
	for _, recipient := range strings.Split(recipients, ",") {
		recipient = strings.TrimSpace(recipient)
		if recipient != "" {
			parsed = append(parsed, recipient)
		}
	}
	return parsed
}

// buildEmailMessage constructs the email message with proper headers
func buildEmailMessage(email *EmailForm, fromEmail string, toRecipients, ccRecipients []string) string {
	var message strings.Builder

	// Headers
	message.WriteString(fmt.Sprintf("From: %s\r\n", fromEmail))
	message.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(toRecipients, ", ")))

	if len(ccRecipients) > 0 {
		message.WriteString(fmt.Sprintf("Cc: %s\r\n", strings.Join(ccRecipients, ", ")))
	}

	message.WriteString(fmt.Sprintf("Subject: %s\r\n", email.Subject))
	message.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")

	// Priority header
	switch email.Priority {
	case "high":
		message.WriteString("X-Priority: 1\r\n")
		message.WriteString("Importance: High\r\n")
	case "low":
		message.WriteString("X-Priority: 5\r\n")
		message.WriteString("Importance: Low\r\n")
	default:
		message.WriteString("X-Priority: 3\r\n")
		message.WriteString("Importance: Normal\r\n")
	}

	// User-Agent
	message.WriteString("User-Agent: CLI-Email-Client\r\n")

	// Empty line to separate headers from body
	message.WriteString("\r\n")

	// Body
	message.WriteString(email.Body)
	message.WriteString("\r\n")

	return message.String()
}
