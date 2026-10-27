// Non-blocking line reader for a file descriptor, so the worker can read its
// commands from the same loop that runs the SDK callbacks, without a thread.
#pragma once

#include <chrono>
#include <cstddef>
#include <optional>
#include <string>

namespace nds {

class LineReader {
public:
    LineReader(int fd, std::size_t maxLineLength);

    // Waits up to timeout for input and reads what is available.
    void poll(std::chrono::milliseconds timeout);

    // Returns the next complete line, without its newline.
    std::optional<std::string> nextLine();

    // True once the input has ended and every line has been returned.
    bool finished() const;

    // Number of lines dropped because they exceeded the maximum length.
    std::size_t takeDroppedLines();

private:
    int fd_;
    std::size_t maxLineLength_;
    std::string buffer_;
    bool discarding_ = false;
    bool eof_ = false;
    std::size_t dropped_ = 0;
};

} // namespace nds
