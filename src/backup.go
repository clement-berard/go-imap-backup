package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/joho/godotenv"
)

type ImapConfig struct {
	Host      string
	Port      string
	User      string
	Password  string
	BackupDir string
}

type Backup struct {
	config    ImapConfig
	client    *client.Client
	delimiter string
	mutex     sync.Mutex
}

func NewBackup(config ImapConfig) *Backup {
	return &Backup{
		config: config,
	}
}

func (b *Backup) Start() error {
	log.Println("Starting IMAP backup...")

	addr := fmt.Sprintf("%s:%s", b.config.Host, b.config.Port)
	log.Printf("Connecting to %s...", addr)

	c, err := client.DialTLS(addr, nil)
	if err != nil {
		return fmt.Errorf("connection error: %v", err)
	}
	b.client = c
	defer b.client.Logout()

	log.Printf("Connected to IMAP server")

	log.Printf("Logging in as %s...", b.config.User)
	if err := b.client.Login(b.config.User, b.config.Password); err != nil {
		return fmt.Errorf("login error: %v", err)
	}
	log.Println("Login successful")

	if err := os.MkdirAll(b.config.BackupDir, 0755); err != nil {
		return fmt.Errorf("error creating directory: %v", err)
	}
	log.Printf("Using backup directory: %s", b.config.BackupDir)

	log.Println("Getting mailbox list...")
	mailboxes := make(chan *imap.MailboxInfo)
	done := make(chan error, 1)
	go func() {
		done <- b.client.List("", "*", mailboxes)
	}()

	var boxes []string
	for mbox := range mailboxes {
		if b.delimiter == "" && mbox.Delimiter != "" {
			b.delimiter = mbox.Delimiter
		}
		boxes = append(boxes, mbox.Name)
	}

	if err := <-done; err != nil {
		return fmt.Errorf("listing error: %v", err)
	}

	log.Println("\nFound folder structure:")
	for _, name := range boxes {
		log.Printf("- %s", name)
	}

	for _, mailboxName := range boxes {
		if err := b.backupMailbox(mailboxName); err != nil {
			log.Printf("Error backing up %s: %v", mailboxName, err)
			continue
		}
	}

	log.Println("Backup completed!")
	return nil
}

func (b *Backup) backupMailbox(mailboxName string) error {
	log.Printf("\nProcessing mailbox: %s", mailboxName)

	relativePath := strings.Split(mailboxName, b.delimiter)
	safePath := make([]string, len(relativePath))
	for i, part := range relativePath {
		safePath[i] = sanitizePath(part)
	}

	mailboxPath := filepath.Join(append([]string{b.config.BackupDir}, safePath...)...)
	if err := os.MkdirAll(mailboxPath, 0755); err != nil {
		return fmt.Errorf("error creating directory %s: %v", mailboxPath, err)
	}

	mbox, err := b.client.Select(mailboxName, true)
	if err != nil {
		return fmt.Errorf("error selecting mailbox: %v", err)
	}

	if mbox.Messages == 0 {
		log.Printf("Empty folder: %s", mailboxName)
		return nil
	}

	log.Printf("Found %d messages in %s", mbox.Messages, mailboxName)

	const batchSize = 100
	for i := uint32(1); i <= mbox.Messages; i += batchSize {
		end := i + batchSize - 1
		if end > mbox.Messages {
			end = mbox.Messages
		}

		if err := b.backupMessageBatch(mailboxName, mailboxPath, i, end); err != nil {
			return fmt.Errorf("error backing up batch %d-%d: %v", i, end, err)
		}
	}

	return nil
}

func (b *Backup) backupMessageBatch(mailboxName, mailboxPath string, start, end uint32) error {
	seqSet := new(imap.SeqSet)
	seqSet.AddRange(start, end)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{section.FetchItem()}

	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)

	go func() {
		done <- b.client.Fetch(seqSet, items, messages)
	}()

	for msg := range messages {
		r := msg.GetBody(section)
		if r == nil {
			log.Printf("Warning: no body for message %d in %s", msg.SeqNum, mailboxName)
			continue
		}

		if err := b.saveMessage(r, mailboxPath, int(msg.SeqNum)); err != nil {
			log.Printf("Error saving message %d: %v", msg.SeqNum, err)
			continue
		}

		log.Printf("\rProgress: %d/%d in %s", msg.SeqNum, end, mailboxName)
	}

	return <-done
}

func (b *Backup) saveMessage(r io.Reader, mailboxPath string, seqNum int) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	filename := fmt.Sprintf("%d_%d.eml", time.Now().UnixNano(), seqNum)
	filepath := filepath.Join(mailboxPath, filename)

	f, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("error creating file: %v", err)
	}
	defer f.Close()

	buf := make([]byte, 32*1024)
	_, err = io.CopyBuffer(f, r, buf)
	if err != nil {
		return fmt.Errorf("error writing message: %v", err)
	}

	return nil
}

func sanitizePath(path string) string {
	invalid := []string{"<", ">", ":", "\"", "/", "\\", "|", "?", "*"}
	result := path

	for _, char := range invalid {
		result = strings.ReplaceAll(result, char, "_")
	}

	return result
}

func main() {
	log.SetFlags(log.Ltime)
	log.Println("Starting IMAP backup tool...")

	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}
	log.Println("Environment loaded")

	config := ImapConfig{
		Host:      os.Getenv("IMAP_HOST"),
		Port:      os.Getenv("IMAP_PORT"),
		User:      os.Getenv("IMAP_USER"),
		Password:  os.Getenv("IMAP_PASSWORD"),
		BackupDir: os.Getenv("BACKUP_DIR"),
	}

	if config.BackupDir == "" {
		config.BackupDir = "email_backup"
	}

	log.Printf("Will backup emails from %s to %s", config.User, config.BackupDir)

	backup := NewBackup(config)
	if err := backup.Start(); err != nil {
		log.Fatal(err)
	}
}
