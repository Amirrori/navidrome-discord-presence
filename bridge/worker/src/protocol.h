// Line protocol between the bridge server and the worker: one JSON object per
// line, commands on stdin and events on stdout.
#pragma once

#include <cstdint>
#include <optional>
#include <string>
#include <variant>

namespace nds {

struct Activity {
    std::string details;
    std::string detailsUrl;
    std::string state;
    std::string stateUrl;
    std::string largeImage;
    std::string largeText;
    std::string largeUrl;
    std::string smallImage;
    std::string smallText;
    std::uint64_t startMs = 0;
    std::uint64_t endMs = 0;
};

struct ConnectCommand {
    std::string token;
};
struct TokenCommand {
    std::string token;
};
struct ActivityCommand {
    Activity activity;
};
struct ClearCommand {};
struct ShutdownCommand {};

using Command = std::variant<ConnectCommand, TokenCommand, ActivityCommand, ClearCommand, ShutdownCommand>;

// Parses one command line. Returns std::nullopt and sets error for invalid input.
std::optional<Command> parseCommand(const std::string& line, std::string& error);

// Event lines written to stdout. Never include tokens in any of them.
std::string readyEvent();
std::string disconnectedEvent(const std::string& error);
std::string errorEvent(const std::string& error);
std::string logEvent(const std::string& level, const std::string& message);

} // namespace nds
