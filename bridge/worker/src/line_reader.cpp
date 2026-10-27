#include "line_reader.h"

#include <array>
#include <cerrno>

#include <poll.h>
#include <unistd.h>

namespace nds {

LineReader::LineReader(int fd, std::size_t maxLineLength)
    : fd_(fd), maxLineLength_(maxLineLength)
{
}

void LineReader::poll(std::chrono::milliseconds timeout)
{
    if (eof_) {
        return;
    }
    pollfd descriptor{fd_, POLLIN, 0};
    const int ready = ::poll(&descriptor, 1, static_cast<int>(timeout.count()));
    if (ready <= 0) {
        return; // timeout, or interrupted by a signal
    }

    std::array<char, 4096> chunk{};
    const ssize_t count = ::read(fd_, chunk.data(), chunk.size());
    if (count > 0) {
        buffer_.append(chunk.data(), static_cast<std::size_t>(count));
    } else if (count == 0 || (errno != EAGAIN && errno != EINTR)) {
        eof_ = true;
    }
}

std::optional<std::string> LineReader::nextLine()
{
    while (true) {
        const auto newline = buffer_.find('\n');
        if (newline == std::string::npos) {
            if (buffer_.size() > maxLineLength_) {
                // Drop the start of an overlong line and skip until its end.
                buffer_.clear();
                if (!discarding_) {
                    discarding_ = true;
                    ++dropped_;
                }
            }
            return std::nullopt;
        }

        std::string line = buffer_.substr(0, newline);
        buffer_.erase(0, newline + 1);
        if (discarding_) {
            discarding_ = false;
            continue;
        }
        if (line.size() > maxLineLength_) {
            ++dropped_;
            continue;
        }
        return line;
    }
}

bool LineReader::finished() const
{
    return eof_ && buffer_.find('\n') == std::string::npos;
}

std::size_t LineReader::takeDroppedLines()
{
    const std::size_t dropped = dropped_;
    dropped_ = 0;
    return dropped;
}

} // namespace nds
