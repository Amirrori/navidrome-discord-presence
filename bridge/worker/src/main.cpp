// nds-worker: holds one user's Discord Social SDK connection for the
// navidrome-discord-status bridge.
//
// Usage: nds-worker <application-id>
//
// Commands arrive as JSON lines on stdin (see protocol.h); the access token is
// only ever sent that way. The worker clears the status and exits on a
// shutdown command or when stdin closes, so it never outlives the server.
// Everything runs on one thread: the loop polls stdin and runs the SDK
// callbacks in turn.

#define DISCORDPP_IMPLEMENTATION
#include "discordpp.h"

#include "line_reader.h"
#include "presence_session.h"
#include "protocol.h"

#include <chrono>
#include <cstdint>
#include <exception>
#include <iostream>
#include <string>
#include <type_traits>
#include <variant>

#include <unistd.h>

namespace {

constexpr auto kLoopInterval = std::chrono::milliseconds(50);
constexpr auto kShutdownGrace = std::chrono::seconds(2);
constexpr std::size_t kMaxLineLength = 64 * 1024;

void emitLine(const std::string& line)
{
    std::cout << line << '\n' << std::flush;
}

void pumpCallbacksFor(std::chrono::milliseconds duration)
{
    const auto until = std::chrono::steady_clock::now() + duration;
    while (std::chrono::steady_clock::now() < until) {
        discordpp::RunCallbacks();
        usleep(static_cast<useconds_t>(std::chrono::microseconds(kLoopInterval).count()));
    }
}

// Applies one command; returns false when the worker should stop.
bool apply(nds::PresenceSession& session, const nds::Command& command)
{
    return std::visit(
        [&session](const auto& cmd) {
            using T = std::decay_t<decltype(cmd)>;
            if constexpr (std::is_same_v<T, nds::ConnectCommand>) {
                session.connect(cmd.token);
            } else if constexpr (std::is_same_v<T, nds::TokenCommand>) {
                session.updateToken(cmd.token);
            } else if constexpr (std::is_same_v<T, nds::ActivityCommand>) {
                session.setActivity(cmd.activity);
            } else if constexpr (std::is_same_v<T, nds::ClearCommand>) {
                session.clear();
            } else if constexpr (std::is_same_v<T, nds::ShutdownCommand>) {
                return false;
            }
            return true;
        },
        command);
}

// Handles all complete lines; returns false when the worker should stop.
bool processInput(nds::LineReader& input, nds::PresenceSession& session)
{
    while (const auto line = input.nextLine()) {
        std::string error;
        const auto command = nds::parseCommand(*line, error);
        if (!command) {
            emitLine(nds::errorEvent(error));
        } else if (!apply(session, *command)) {
            return false;
        }
    }
    if (const auto dropped = input.takeDroppedLines(); dropped > 0) {
        emitLine(nds::errorEvent(std::to_string(dropped) + " overlong command line(s) ignored"));
    }
    return !input.finished();
}

} // namespace

int main(int argc, char** argv)
{
    if (argc != 2) {
        std::cerr << "usage: " << argv[0] << " <application-id>\n";
        return 2;
    }
    std::uint64_t applicationId = 0;
    try {
        applicationId = std::stoull(argv[1]);
    } catch (const std::exception&) {
        std::cerr << "invalid application id\n";
        return 2;
    }

    nds::PresenceSession session(applicationId, emitLine);
    nds::LineReader input(STDIN_FILENO, kMaxLineLength);

    bool running = true;
    while (running) {
        discordpp::RunCallbacks();
        input.poll(kLoopInterval);
        running = processInput(input, session);
    }

    session.disconnect();
    pumpCallbacksFor(kShutdownGrace);
    emitLine(nds::logEvent("info", "shutdown complete"));
    return 0;
}
