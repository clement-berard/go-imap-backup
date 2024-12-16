package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
	"github.com/joho/godotenv"
)

type EmailInfo struct {
	Mailbox  string
	Uid      uint32
	Subject  string
	Date     time.Time
	Size     uint32
	Hash     string
}

type DuplicateGroup struct {
	Emails []EmailInfo
	Hash   string
}

type IMAPManager struct {
	client     *client.Client
	trashbox   string
}

func connectIMAP() (*IMAPManager, error) {
	host := os.Getenv("IMAP_HOST")
	port := os.Getenv("IMAP_PORT")
	user := os.Getenv("IMAP_USER")
	pass := os.Getenv("IMAP_PASSWORD")

	addr := fmt.Sprintf("%s:%s", host, port)
	log.Printf("Connecting to %s...", addr)

	c, err := client.DialTLS(addr, nil)
	if err != nil {
		return nil, fmt.Errorf("connection error: %v", err)
	}

	if err := c.Login(user, pass); err != nil {
		return nil, fmt.Errorf("login error: %v", err)
	}
	log.Printf("Connected as %s", user)

	// Find trash folder
	mailboxes := make(chan *imap.MailboxInfo)
	done := make(chan error, 1)
	go func() {
		done <- c.List("", "*", mailboxes)
	}()

	trashbox := ""
	trashNames := []string{"Trash", "TRASH", "Corbeille", "Deleted Items", "[Gmail]/Trash", "[Google Mail]/Trash"}
	for m := range mailboxes {
		for _, t := range trashNames {
			if strings.EqualFold(m.Name, t) {
				trashbox = m.Name
				break
			}
		}
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("error listing mailboxes: %v", err)
	}

	if trashbox == "" {
		return nil, fmt.Errorf("could not find trash folder")
	}

	return &IMAPManager{
		client:   c,
		trashbox: trashbox,
	}, nil
}

func computeEmailHash(msg *mail.Reader) (string, error) {
	h := sha256.New()

	// Parse header for relevant fields
	header := msg.Header
	if subject := header.Get("Subject"); subject != "" {
		h.Write([]byte(subject))
	}
	if date := header.Get("Date"); date != "" {
		h.Write([]byte(date))
	}

	// Read each part of the email
	for {
		p, err := msg.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		contentType := p.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(contentType, "text/plain"):
			if content, err := io.ReadAll(p.Body); err == nil {
				h.Write(content)
			}
		case strings.HasPrefix(contentType, "text/html"):
			if content, err := io.ReadAll(p.Body); err == nil {
				h.Write(content)
			}
		case strings.HasPrefix(contentType, "application/"):
			// Include attachments in hash
			if content, err := io.ReadAll(p.Body); err == nil {
				h.Write(content)
			}
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func (im *IMAPManager) Close() {
	im.client.Logout()
}

func (im *IMAPManager) listMailboxes() ([]string, error) {
	mailboxes := make(chan *imap.MailboxInfo)
	done := make(chan error, 1)
	go func() {
		done <- im.client.List("", "*", mailboxes)
	}()

	var boxes []string
	for m := range mailboxes {
		if !strings.EqualFold(m.Name, im.trashbox) {
			boxes = append(boxes, m.Name)
		}
	}

	return boxes, <-done
}

func (im *IMAPManager) scanMailbox(mailboxName string) ([]EmailInfo, error) {
	log.Printf("Scanning mailbox: %s", mailboxName)

	mbox, err := im.client.Select(mailboxName, true)
	if err != nil {
		return nil, fmt.Errorf("error selecting mailbox: %v", err)
	}

	if mbox.Messages == 0 {
		return nil, nil
	}

	var emails []EmailInfo
	batchSize := uint32(100)

	for i := uint32(1); i <= mbox.Messages; i += batchSize {
		from := i
		to := i + batchSize - 1
		if to > mbox.Messages {
			to = mbox.Messages
		}

		seqSet := new(imap.SeqSet)
		seqSet.AddRange(from, to)

		messages := make(chan *imap.Message, 10)
		done := make(chan error, 1)

		items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchBody, imap.FetchBodyStructure}

		go func() {
			done <- im.client.Fetch(seqSet, items, messages)
		}()

		for msg := range messages {
			section := &imap.BodySectionName{}
			r := msg.GetBody(section)
			if r == nil {
				continue
			}

			// Parse email
			mr, err := mail.CreateReader(r)
			if err != nil {
				continue
			}

			// Compute content-based hash
			hash, err := computeEmailHash(mr)
			if err != nil {
				continue
			}

			emailInfo := EmailInfo{
				Mailbox: mailboxName,
				Uid:     msg.Uid,
				Subject: msg.Envelope.Subject,
				Date:    msg.Envelope.Date,
				Size:    msg.Size,
				Hash:    hash,
			}
			emails = append(emails, emailInfo)

			fmt.Printf("\rProcessed %d/%d in %s", len(emails), mbox.Messages, mailboxName)
		}

		if err := <-done; err != nil {
			return nil, fmt.Errorf("error fetching messages: %v", err)
		}
	}

	fmt.Println()
	return emails, nil
}

func (im *IMAPManager) moveToTrash(email EmailInfo) error {
	_, err := im.client.Select(email.Mailbox, false)
	if err != nil {
		return err
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(email.Uid)

	return im.client.Move(seqSet, im.trashbox)
}

func findDuplicates(emails []EmailInfo) []DuplicateGroup {
	hashMap := make(map[string][]EmailInfo)

	for _, email := range emails {
		hashMap[email.Hash] = append(hashMap[email.Hash], email)
	}

	var groups []DuplicateGroup
	for hash, duplicates := range hashMap {
		if len(duplicates) > 1 {
			groups = append(groups, DuplicateGroup{
				Emails: duplicates,
				Hash:   hash,
			})
		}
	}

	return groups
}

func promptForChoice(group DuplicateGroup, dryRun bool) (int, error) {
	fmt.Printf("\n=== Duplicate Group ===\n")
	fmt.Printf("Subject: %s\n", group.Emails[0].Subject)
	fmt.Printf("Date: %s\n", group.Emails[0].Date.Format("2006-01-02 15:04:05"))
	fmt.Printf("Found %d copies:\n\n", len(group.Emails))

	for i, email := range group.Emails {
		fmt.Printf("%d) [%s] %s (%d KB) - %s\n",
			i+1,
			email.Mailbox,
			email.Subject,
			email.Size/1024,
			email.Date.Format("2006-01-02 15:04:05"),
		)
	}

	if dryRun {
		return 0, nil
	}

	fmt.Printf("\nEnter number to keep (1-%d) or 's' to skip: ", len(group.Emails))

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return -1, err
	}

	input = strings.TrimSpace(input)
	if input == "s" || input == "S" {
		return -1, nil
	}

	choice, err := strconv.Atoi(input)
	if err != nil || choice < 1 || choice > len(group.Emails) {
		return -1, fmt.Errorf("invalid choice")
	}

	return choice - 1, nil
}

func confirmActions(planned []EmailInfo) bool {
	fmt.Printf("\n=== Summary of Actions ===\n")
	fmt.Printf("Messages to move to trash: %d\n\n", len(planned))

	for i, email := range planned {
		fmt.Printf("%d) Move to trash: [%s] %s (%s)\n",
			i+1,
			email.Mailbox,
			email.Subject,
			email.Date.Format("2006-01-02 15:04:05"),
		)
	}

	fmt.Print("\nDo you want to proceed with these actions? (yes/no): ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(strings.ToLower(input)) == "yes"
}

func main() {
	dryRun := flag.Bool("dry-run", false, "Show what would be done without making any changes")
	flag.Parse()

	if *dryRun {
		fmt.Println("Running in dry-run mode - no changes will be made")
	}

	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nInterrupted. Exiting safely...")
		os.Exit(0)
	}()

	imap, err := connectIMAP()
	if err != nil {
		log.Fatalf("Failed to connect to IMAP: %v", err)
	}
	defer imap.Close()

	log.Printf("Using trash folder: %s", imap.trashbox)

	mailboxes, err := imap.listMailboxes()
	if err != nil {
		log.Fatalf("Error listing mailboxes: %v", err)
	}

	var allEmails []EmailInfo
	for _, mailbox := range mailboxes {
		emails, err := imap.scanMailbox(mailbox)
		if err != nil {
			log.Printf("Error scanning %s: %v", mailbox, err)
			continue
		}
		allEmails = append(allEmails, emails...)
	}

	duplicateGroups := findDuplicates(allEmails)
	fmt.Printf("\nFound %d groups of duplicates\n", len(duplicateGroups))

	var plannedMoves []EmailInfo

	for i, group := range duplicateGroups {
		fmt.Printf("\nProcessing group %d/%d\n", i+1, len(duplicateGroups))

		choice, err := promptForChoice(group, *dryRun)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		if choice == -1 {
			fmt.Println("Skipping this group")
			continue
		}

		// Add all emails except the chosen one to planned moves
		for j, email := range group.Emails {
			if j != choice {
				plannedMoves = append(plannedMoves, email)
			}
		}
	}

	if len(plannedMoves) == 0 {
		fmt.Println("\nNo actions to perform")
		return
	}

	if *dryRun {
		fmt.Println("\n=== Dry Run Summary ===")
		for _, email := range plannedMoves {
			fmt.Printf("Would move to trash: [%s] %s (%s)\n",
				email.Mailbox,
				email.Subject,
				email.Date.Format("2006-01-02 15:04:05"),
			)
		}
		return
	}

	if !confirmActions(plannedMoves) {
		fmt.Println("Operation cancelled")
		return
	}

	fmt.Println("\nMoving messages to trash...")
	for i, email := range plannedMoves {
		fmt.Printf("\rProgress: %d/%d", i+1, len(plannedMoves))
		if err := imap.moveToTrash(email); err != nil {
			fmt.Printf("\nError moving message to trash: %v\n", err)
		}
	}

	fmt.Println("\nAll actions completed!")
}
