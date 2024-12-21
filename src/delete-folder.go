package main

import (
    "bufio"
    "flag"
    "fmt"
    "log"
    "os"
    "sort"
    "strings"
    "syscall"
    "os/signal"
    "time"

    "github.com/emersion/go-imap"
    "github.com/emersion/go-imap/client"
    "github.com/joho/godotenv"
)

type MessageInfo struct {
    Subject string
    Date    string
}

type MailboxInfo struct {
    Name     string
    Messages uint32
    MessagesList []MessageInfo
}

type IMAPManager struct {
    client *client.Client
    dryRun bool
    host     string
    port     string
    user     string
    password string
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

    return &IMAPManager{
        client: c,
        host: host,
        port: port,
        user: user,
        password: pass,
    }, nil
}

func (im *IMAPManager) reconnect() error {
    if im.client != nil {
        im.client.Logout()
    }

    addr := fmt.Sprintf("%s:%s", im.host, im.port)
    log.Printf("Reconnecting to %s...", addr)

    c, err := client.DialTLS(addr, nil)
    if err != nil {
        return fmt.Errorf("connection error: %v", err)
    }

    if err := c.Login(im.user, im.password); err != nil {
        return fmt.Errorf("login error: %v", err)
    }

    im.client = c
    log.Printf("Reconnected successfully")
    return nil
}

func (im *IMAPManager) Close() {
    if im.client != nil {
        im.client.Logout()
    }
}

func (im *IMAPManager) listAllMailboxes() ([]string, error) {
    log.Printf("Listing all mailboxes...")
    mailboxes := make(chan *imap.MailboxInfo)
    done := make(chan error, 1)

    go func() {
        done <- im.client.List("", "*", mailboxes)
    }()

    var boxes []string
    for m := range mailboxes {
        boxes = append(boxes, m.Name)
    }

    if err := <-done; err != nil {
        return nil, fmt.Errorf("error listing mailboxes: %v", err)
    }

    log.Printf("Found %d mailboxes in total", len(boxes))
    return boxes, nil
}

func (im *IMAPManager) getMailboxMessages(mailboxName string) ([]MessageInfo, error) {
    mbox, err := im.client.Select(mailboxName, true)
    if err != nil {
        return nil, err
    }

    if mbox.Messages == 0 {
        return []MessageInfo{}, nil
    }

    seqSet := new(imap.SeqSet)
    seqSet.AddRange(1, mbox.Messages)

    messages := make(chan *imap.Message, 10)
    done := make(chan error, 1)

    go func() {
        done <- im.client.Fetch(seqSet, []imap.FetchItem{imap.FetchEnvelope}, messages)
    }()

    var messagesList []MessageInfo
    for msg := range messages {
        messagesList = append(messagesList, MessageInfo{
            Subject: msg.Envelope.Subject,
            Date:    msg.Envelope.Date.Format("2006-01-02 15:04:05"),
        })
    }

    if err := <-done; err != nil {
        return nil, err
    }

    return messagesList, nil
}

func (im *IMAPManager) findMailboxesForDeletion(prefix string, withMessages bool) ([]MailboxInfo, error) {
    log.Printf("Looking for mailboxes starting with: %s", prefix)

    allBoxes, err := im.listAllMailboxes()
    if err != nil {
        return nil, err
    }

    var toDelete []MailboxInfo
    for _, name := range allBoxes {
        if strings.HasPrefix(name, prefix) {
            log.Printf("Found matching mailbox: %s", name)

            mbox, err := im.client.Select(name, true)
            if err != nil {
                log.Printf("Error selecting mailbox %s: %v", name, err)
                continue
            }

            mailboxInfo := MailboxInfo{
                Name:     name,
                Messages: mbox.Messages,
            }

            if withMessages && mbox.Messages > 0 {
                messages, err := im.getMailboxMessages(name)
                if err != nil {
                    log.Printf("Error getting messages for %s: %v", name, err)
                } else {
                    mailboxInfo.MessagesList = messages
                }
            }

            toDelete = append(toDelete, mailboxInfo)
            log.Printf("- %s (%d messages)", name, mbox.Messages)
        }
    }

    if len(toDelete) == 0 {
        return nil, fmt.Errorf("no mailboxes found matching: %s", prefix)
    }

    log.Printf("Found %d mailboxes to delete", len(toDelete))
    return toDelete, nil
}

func (im *IMAPManager) deleteMailbox(name string) error {
    for attempts := 0; attempts < 3; attempts++ {
        if attempts > 0 {
            log.Printf("Retry attempt %d for %s", attempts, name)
            if err := im.reconnect(); err != nil {
                log.Printf("Reconnection failed: %v", err)
                time.Sleep(time.Second * 2)
                continue
            }
        }

        log.Printf("Deleting mailbox: %s", name)

        mbox, err := im.client.Select(name, false)
        if err != nil {
            if strings.Contains(err.Error(), "Not logged in") {
                time.Sleep(time.Second * 2)
                continue
            }
            return fmt.Errorf("error selecting mailbox: %v", err)
        }

        if mbox.Messages > 0 {
            log.Printf("Marking %d messages for deletion", mbox.Messages)
            seqSet := new(imap.SeqSet)
            seqSet.AddRange(1, mbox.Messages)

            item := imap.FormatFlagsOp(imap.AddFlags, true)
            flags := []interface{}{imap.DeletedFlag}
            if err := im.client.Store(seqSet, item, flags, nil); err != nil {
                if strings.Contains(err.Error(), "Not logged in") {
                    time.Sleep(time.Second * 2)
                    continue
                }
                return fmt.Errorf("error marking messages as deleted: %v", err)
            }

            log.Printf("Expunging messages...")
            if err := im.client.Expunge(nil); err != nil {
                if strings.Contains(err.Error(), "Not logged in") {
                    time.Sleep(time.Second * 2)
                    continue
                }
                return fmt.Errorf("error expunging messages: %v", err)
            }
        }

        log.Printf("Deleting the mailbox itself...")
        if err := im.client.Delete(name); err != nil {
            if strings.Contains(err.Error(), "Not logged in") {
                time.Sleep(time.Second * 2)
                continue
            }
            return fmt.Errorf("error deleting mailbox: %v", err)
        }

        return nil
    }

    return fmt.Errorf("failed to delete mailbox after 3 attempts")
}

func sortMailboxesByDepth(mailboxes []MailboxInfo) []MailboxInfo {
    sorted := append([]MailboxInfo{}, mailboxes...)
    sort.Slice(sorted, func(i, j int) bool {
        depthI := strings.Count(sorted[i].Name, "/")
        depthJ := strings.Count(sorted[j].Name, "/")
        return depthI > depthJ
    })
    return sorted
}

func showMessagesDetails(mailboxes []MailboxInfo) {
    fmt.Println("\n=== Detailed Messages List ===")
    for _, m := range mailboxes {
        fmt.Printf("\nFolder: %s (%d messages)\n", m.Name, m.Messages)
        if len(m.MessagesList) > 0 {
            for i, msg := range m.MessagesList {
                fmt.Printf("%d) [%s] %s\n", i+1, msg.Date, msg.Subject)
            }
        }
        fmt.Println(strings.Repeat("-", 50))
    }
}

func confirmDeletion(mailboxes []MailboxInfo, folderName string) bool {
    fmt.Printf("\n=== Folders to be deleted ===\n")
    fmt.Printf("Base folder: %s\n\n", folderName)

    var totalMessages uint32
    for _, m := range mailboxes {
        fmt.Printf("- %s (%d messages)\n", m.Name, m.Messages)
        totalMessages += m.Messages
    }

    fmt.Printf("\nTotal: %d folders, %d messages\n", len(mailboxes), totalMessages)
    fmt.Print("\nDo you want to proceed with deletion? (yes/no): ")

    reader := bufio.NewReader(os.Stdin)
    input, _ := reader.ReadString('\n')
    return strings.TrimSpace(strings.ToLower(input)) == "yes"
}

func askShowDetails() bool {
    fmt.Print("\nWould you like to see the detailed list of all messages? (yes/no): ")
    reader := bufio.NewReader(os.Stdin)
    input, _ := reader.ReadString('\n')
    return strings.TrimSpace(strings.ToLower(input)) == "yes"
}

func main() {
    dryRun := flag.Bool("dry-run", false, "Show what would be deleted without making changes")
    flag.Parse()

    if flag.NArg() != 1 {
        log.Fatal("Usage: delete-folder [--dry-run] folder_name")
    }
    folderName := flag.Arg(0)

    if *dryRun {
        log.Println("Running in dry-run mode - no changes will be made")
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

    mailboxes, err := imap.findMailboxesForDeletion(folderName, *dryRun)
    if err != nil {
        log.Fatalf("Error finding mailboxes: %v", err)
    }

    if *dryRun {
        log.Println("\n=== Dry Run - Would delete ===")
        for _, m := range mailboxes {
            log.Printf("Would delete: %s (%d messages)", m.Name, m.Messages)
        }

        if askShowDetails() {
            showMessagesDetails(mailboxes)
        }
        return
    }

    if !confirmDeletion(mailboxes, folderName) {
        fmt.Println("Operation cancelled")
        return
    }

    sortedMailboxes := sortMailboxesByDepth(mailboxes)

    fmt.Println("\nDeleting folders...")
    for i, m := range sortedMailboxes {
        fmt.Printf("\rProgress: %d/%d - Deleting %s", i+1, len(sortedMailboxes), m.Name)
        if err := imap.deleteMailbox(m.Name); err != nil {
            log.Printf("\nError deleting %s: %v\n", m.Name, err)
            continue
        }
    }

    fmt.Println("\nAll folders deleted successfully!")
}
