#!/bin/bash

# Array of source files to build (specify their paths relative to this script)
SOURCE_FILES=("src/backup.go")

# Directory to store the compiled binaries
OUTPUT_DIR="builds"

# Create the output directory if it doesn't exist
mkdir -p "$OUTPUT_DIR"

# List of supported GOOS and GOARCH (modify as needed)
GOOS_LIST=("linux" "darwin" "windows" "freebsd")
GOARCH_LIST=("amd64" "arm64" "arm" "386" "riscv64" "ppc64le" "s390x")

# Function to check if GOOS/GOARCH is supported
is_supported() {
  local GOOS=$1
  local GOARCH=$2
  go tool dist list | grep -q "^${GOOS}/${GOARCH}$"
}

# Build for each source file
for SOURCE_FILE in "${SOURCE_FILES[@]}"; do
  # Extract the base name of the source file without the extension
  PROGRAM_NAME=$(basename "$SOURCE_FILE" .go)

  # Build for each combination of GOOS and GOARCH
  for GOOS in "${GOOS_LIST[@]}"; do
    for GOARCH in "${GOARCH_LIST[@]}"; do
      if is_supported "$GOOS" "$GOARCH"; then
        OUTPUT_FILE="${OUTPUT_DIR}/${PROGRAM_NAME}_${GOOS}_${GOARCH}"

        # Add .exe extension for Windows
        if [ "$GOOS" == "windows" ]; then
          OUTPUT_FILE+=".exe"
        fi

        echo "Building $PROGRAM_NAME for $GOOS/$GOARCH..."

        # Run the build command
        env GOOS=$GOOS GOARCH=$GOARCH go build -o "$OUTPUT_FILE" "$SOURCE_FILE"

        # Check if the build was successful
        if [ $? -ne 0 ]; then
          echo "Failed to build $PROGRAM_NAME for $GOOS/$GOARCH"
        else
          echo "Built: $OUTPUT_FILE"
        fi
      else
        echo "Skipping unsupported GOOS/GOARCH pair: $GOOS/$GOARCH"
      fi
    done
  done
done

echo "Build process completed. Binaries are in the '$OUTPUT_DIR' directory."
