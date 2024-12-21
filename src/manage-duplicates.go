package main

import (
    "bufio"
    "bytes"
    "crypto/sha256"
    "encoding/hex"
    "flag"
    "fmt"
    "log"
    "os"
    "os/signal"
    "strconv"
    "strings"
    "syscall"
    "time"

    "github.com/emersion/go-imap"
    "github.com/emersion/go-imap/client"
    "github.com/joho/godotenv"
)

type EmailInfo struct {
    Mailbox  string
    Uid      uint32
    Subject  string
    Date     time.Time
    Size     uint32
    Hash     string
    Content  string
}

type DuplicateGroup struct {
    Emails []EmailInfo
    Hash   string
}

type IMAPManager struct {
    client       *client.Client
    targetFolder string
    excludedFolders []string
}

func connectIMAP() (*IMAPManager, error) {
    host := os.Getenv("IMAP_HOST")
    port := os.Getenv("IMAP_PORT")
    user := os.Getenv("IMAP_USER")
    pass := os.Getenv("IMAP_PASSWORD")
    targetFolder := os.Getenv("TARGET_FOLDER")

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

    excludedFolders := []string{
        "trash", "corbeille", "deleted", "deleted messages",
        "deleted items", "elements supprimes", "éléments supprimés",
        "bin", "junk", "spam", "[gmail]/trash",
        "[google mail]/trash", "[gmail]/corbeille",
        "[gmail]/spam", "[gmail]/junk",
    }

    return &IMAPManager{
        client: c,
        targetFolder: targetFolder,
        excludedFolders: excludedFolders,
    }, nil
}

func (im *IMAPManager) isExcludedFolder(name string) bool {
    nameLower := strings.ToLower(name)
    for _, excluded := range im.excludedFolders {
        if strings.Contains(nameLower, excluded) {
            log.Printf("Excluding folder: %s", name)
            return true
        }
    }
    return false
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
        // Ignore les dossiers exclus
        if im.isExcludedFolder(m.Name) {
            continue
        }

        // Filtre selon targetFolder si défini
        if im.targetFolder != "" {
            if strings.HasPrefix(m.Name, im.targetFolder) {
                boxes = append(boxes, m.Name)
            }
        } else {
            boxes = append(boxes, m.Name)
        }
    }

    if err := <-done; err != nil {
        return nil, err
    }

    if im.targetFolder != "" && len(boxes) == 0 {
        return nil, fmt.Errorf("no mailboxes found matching target folder: %s", im.targetFolder)
    }

    log.Printf("Found %d mailboxes to analyze (excluding trash/spam folders)", len(boxes))
    return boxes, nil
}

func (im *IMAPManager) scanMailbox(mailboxName string) ([]EmailInfo, error) {
    log.Printf("Scanning mailbox: %s", mailboxName)

    mbox, err := im.client.Select(mailboxName, true)
    if err != nil {
        return nil, fmt.Errorf("error selecting mailbox: %v", err)
    }

    if mbox.Messages == 0 {
        log.Printf("Mailbox %s is empty", mailboxName)
        return nil, nil
    }

    log.Printf("Found %d messages in %s", mbox.Messages, mailboxName)

    var emails []EmailInfo
    batchSize := uint32(50)

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

        go func() {
            done <- im.client.Fetch(seqSet, []imap.FetchItem{imap.FetchRFC822, imap.FetchUid, imap.FetchEnvelope}, messages)
        }()

        for msg := range messages {
            if msg == nil {
                log.Printf("Warning: nil message received")
                continue
            }

            var msgData []byte
            for _, r := range msg.Body {
                if r == nil {
                    continue
                }
                buf := new(bytes.Buffer)
                _, err := buf.ReadFrom(r)
                if err != nil {
                    log.Printf("Warning: error reading message body: %v", err)
                    continue
                }
                msgData = buf.Bytes()
                break
            }

            if len(msgData) == 0 {
                log.Printf("Warning: empty message UID %d", msg.Uid)
                continue
            }

            hash := sha256.Sum256(msgData)

            emailInfo := EmailInfo{
                Mailbox: mailboxName,
                Uid:     msg.Uid,
                Subject: msg.Envelope.Subject,
                Date:    msg.Envelope.Date,
                Size:    uint32(len(msgData)),
                Hash:    hex.EncodeToString(hash[:]),
                Content: string(msgData[:100]),
            }
            emails = append(emails, emailInfo)

            fmt.Printf("\rProcessed UID %d in %s (%d bytes)", msg.Uid, mailboxName, len(msgData))
        }

        if err := <-done; err != nil {
            return nil, fmt.Errorf("error fetching messages: %v", err)
        }
    }

    log.Printf("\nSuccessfully processed %d messages in %s", len(emails), mailboxName)
    return emails, nil
}

func (im *IMAPManager) deleteEmail(email EmailInfo) error {
    log.Printf("Deleting email [%s] UID %d...", email.Mailbox, email.Uid)

    _, err := im.client.Select(email.Mailbox, false)
    if err != nil {
        return fmt.Errorf("error selecting mailbox: %v", err)
    }

    seqSet := new(imap.SeqSet)
    seqSet.AddNum(email.Uid)

    item := imap.FormatFlagsOp(imap.AddFlags, true)
    flags := []interface{}{imap.DeletedFlag}
    if err := im.client.UidStore(seqSet, item, flags, nil); err != nil {
        return fmt.Errorf("error marking message as deleted: %v", err)
    }

    if err := im.client.Expunge(nil); err != nil {
        return fmt.Errorf("error expunging mailbox: %v", err)
    }

    log.Printf("Successfully deleted email [%s] UID %d", email.Mailbox, email.Uid)
    return nil
}

func findDuplicates(emails []EmailInfo) []DuplicateGroup {
    hashMap := make(map[string][]EmailInfo)

    log.Printf("Analyzing %d emails for duplicates...", len(emails))

    for _, email := range emails {
        if email.Hash != "" {
            hashMap[email.Hash] = append(hashMap[email.Hash], email)
        }
    }

    var groups []DuplicateGroup
    for hash, duplicates := range hashMap {
        if len(duplicates) > 1 {
            log.Printf("Found duplicate group with %d emails: %s",
                len(duplicates),
                duplicates[0].Subject)
            groups = append(groups, DuplicateGroup{
                Emails: duplicates,
                Hash:   hash,
            })
        }
    }

    return groups
}

func promptForChoice(group DuplicateGroup, currentGroup, totalGroups int, dryRun bool, autoMode bool) (int, bool) {
    if autoMode {
        return 0, false
    }

    fmt.Printf("\n=== Duplicate Group (%d/%d) ===\n", currentGroup, totalGroups)
    fmt.Printf("Subject: %s\n", group.Emails[0].Subject)
    fmt.Printf("Date: %s\n", group.Emails[0].Date.Format("2006-01-02 15:04:05"))
    fmt.Printf("Content preview: %s\n", group.Emails[0].Content)
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
        return 0, false
    }

    fmt.Printf("\nEnter number to keep (1-%d), 's' to skip, or 'q' to see summary: ", len(group.Emails))

    reader := bufio.NewReader(os.Stdin)
    input, err := reader.ReadString('\n')
    if err != nil {
        return -1, false
    }

    input = strings.TrimSpace(input)
    if input == "q" || input == "Q" {
        return -1, true
    }
    if input == "s" || input == "S" {
        return -1, false
    }

    choice, err := strconv.Atoi(input)
    if err != nil || choice < 1 || choice > len(group.Emails) {
        return -1, false
    }

    return choice - 1, false
}

func confirmActions(planned []EmailInfo) bool {
    fmt.Printf("\n=== Summary of Actions ===\n")
    fmt.Printf("Messages to delete: %d\n\n", len(planned))

    for i, email := range planned {
        fmt.Printf("%d) Delete: [%s] %s (%s)\n",
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
    autoMode := flag.Bool("auto", false, "Automatically select first email in each group")
    flag.Parse()

    if *dryRun {
        fmt.Println("Running in dry-run mode - no changes will be made")
    }
    if *autoMode {
        fmt.Println("Running in auto mode - will select first email in each group")
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

    if imap.targetFolder != "" {
        log.Printf("Using target folder: %s", imap.targetFolder)
    }

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

    var plannedDeletes []EmailInfo

    for i, group := range duplicateGroups {
        choice, quit := promptForChoice(group, i+1, len(duplicateGroups), *dryRun, *autoMode)

        if choice != -1 {
            for j, email := range group.Emails {
                if j != choice {
                    plannedDeletes = append(plannedDeletes, email)
                }
            }
        }

        if quit && !*autoMode {
            fmt.Println("\nJumping to summary...")
            break
        }
        if choice == -1 && !quit && !*autoMode {
            fmt.Println("Skipping this group")
            continue
        }
    }

    if len(plannedDeletes) == 0 {
        fmt.Println("\nNo actions to perform")
        return
    }

    if *dryRun {
        fmt.Println("\n=== Dry Run Summary ===")
        for _, email := range plannedDeletes {
            fmt.Printf("Would delete: [%s] %s (%s)\n",
                email.Mailbox,
                email.Subject,
                email.Date.Format("2006-01-02 15:04:05"),
            )
        }
        return
    }

    if !confirmActions(plannedDeletes) {
        fmt.Println("Operation cancelled")
        return
    }

    fmt.Println("\nDeleting messages...")
    for i, email := range plannedDeletes {
        fmt.Printf("\rProgress: %d/%d", i+1, len(plannedDeletes))
        if err := imap.deleteEmail(email); err != nil {
            fmt.Printf("\nError deleting message: %v\n", err)
        }
    }

    fmt.Println("\nAll actions completed!")
}
