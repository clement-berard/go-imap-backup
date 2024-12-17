# IMAP Email Management Tools

A collection of Go tools for managing IMAP emails, featuring backup capabilities and duplicate detection/cleanup.

## Features

- **Email Backup**
    - Full mailbox backup with folder structure preservation
    - Selective folder backup support
    - Progress tracking and error handling
    - Maintains email metadata and attachments

- **Duplicate Management**
    - Intelligent duplicate detection using content hashing
    - Interactive or automatic duplicate resolution
    - Dry-run mode for safe testing
    - Detailed action summaries

## Installation

### Using pre-built binaries (recommended)

Download the latest release that matches your system from the [releases page](https://github.com/clement-berard/go-imap-backup/releases).

```bash
# Example for Linux x64
wget https://github.com/clement-berard/go-imap-backup/releases/latest/download/go-imap-backup-linux-amd64
chmod +x go-imap-backup-linux-amd64

# Example for macOS x64
wget https://github.com/clement-berard/go-imap-backup/releases/latest/download/go-imap-backup-darwin-amd64
chmod +x go-imap-backup-darwin-amd64
```

### Building from source

If you prefer building from source:

```bash
git clone https://github.com/clement-berard/go-imap-backup
cd go-imap-backup
go build
```

## Configuration

Create a `.env` file in the same directory as the binary:

```env
IMAP_HOST=imap.example.com
IMAP_PORT=993
IMAP_USER=your.email@example.com
IMAP_PASSWORD=your_password
BACKUP_DIR=email_backup
TARGET_FOLDER=Optional/Specific/Folder  # Optional: focus on specific folder
```

## Usage

### Email Backup

```bash
# Using pre-built binary
./go-imap-backup-[your-platform] backup

# Or if built from source
./go-imap-backup backup
```

The backup will create a directory structure like:
```
email_backup/
├── INBOX/
│   ├── 1703011234_1.eml
│   └── 1703011235_2.eml
├── Sent/
│   └── 1703011236_1.eml
└── Work/
    └── Project/
        └── 1703011237_1.eml
```

Each `.eml` file contains a complete email with all metadata and attachments.

### Duplicate Management

```bash
# Interactive mode
./go-imap-backup-[your-platform] duplicates

# Automatic mode (selects first email in each duplicate group)
./go-imap-backup-[your-platform] duplicates --auto

# Dry run (shows what would be done without making changes)
./go-imap-backup-[your-platform] duplicates --dry-run

# Automatic dry run
./go-imap-backup-[your-platform] duplicates --auto --dry-run
```

#### Interactive Mode Options
- Enter number (1-N): Keep that email, delete others in group
- `s`: Skip current group
- `q`: Jump to summary
- `a`: Auto-select first email in current group

#### Targeting Specific Folders
Use the `TARGET_FOLDER` environment variable to focus on a specific folder:
```env
TARGET_FOLDER=Work/Project
```

## How Duplicate Detection Works

1. **Content Hash**: Creates a SHA-256 hash of each email's content
2. **Hash Matching**: Groups emails with identical hashes
3. **Verification**: Shows duplicate groups with details:
    - Subject
    - Date
    - Size
    - Mailbox location
    - Content preview

## Safety Features

- Read-only scanning
- Dry-run mode
- Confirmation prompts
- Safe interruption handling
- Error logging
- Progress tracking

## Important Notes

- Always backup your emails before using the duplicate management tool
- Test with `--dry-run` first
- Some email providers might have specific IMAP settings or limitations
- Large mailboxes might take significant time to process

## Known Issues

- Gmail requires App Password for IMAP access
- Some IMAP servers might have connection limits
- Large attachments might require more memory

## Support

For issues and feature requests, please use the [GitHub issue tracker](https://github.com/clement-berard/go-imap-backup/issues).

## License

This project is licensed under the MIT License.
