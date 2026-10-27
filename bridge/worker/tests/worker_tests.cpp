// Unit tests for the parts of the worker that don't need the Discord SDK.
// Build with -DNDS_BUILD_TESTS=ON (add -DNDS_BUILD_WORKER=OFF without the SDK).

#include "line_reader.h"
#include "protocol.h"

#include <chrono>
#include <cstdio>
#include <functional>
#include <string>
#include <variant>
#include <vector>

#include <unistd.h>

namespace {

int failures = 0;

#define CHECK(condition)                                                                 \
    do {                                                                                 \
        if (!(condition)) {                                                              \
            std::fprintf(stderr, "%s:%d: check failed: %s\n", __FILE__, __LINE__, #condition); \
            ++failures;                                                                  \
        }                                                                                \
    } while (false)

std::optional<nds::Command> parse(const std::string& line)
{
    std::string error;
    return nds::parseCommand(line, error);
}

std::string parseError(const std::string& line)
{
    std::string error;
    nds::parseCommand(line, error);
    return error;
}

void testParsesEveryCommand()
{
    const auto connect = parse(R"({"op":"connect","token":"abc"})");
    CHECK(connect && std::get<nds::ConnectCommand>(*connect).token == "abc");

    const auto token = parse(R"({"op":"token","token":"def"})");
    CHECK(token && std::get<nds::TokenCommand>(*token).token == "def");

    CHECK(parse(R"({"op":"clear"})") && std::holds_alternative<nds::ClearCommand>(*parse(R"({"op":"clear"})")));
    CHECK(parse(R"({"op":"shutdown"})") && std::holds_alternative<nds::ShutdownCommand>(*parse(R"({"op":"shutdown"})")));
}

void testParsesActivity()
{
    const auto command = parse(R"({"op":"activity","activity":{"details":"Undone","state":"Weezer",)"
                               R"("largeImage":"https://img/a.jpg","startMs":1000,"endMs":5000}})");
    CHECK(command.has_value());
    if (!command) {
        return;
    }
    const auto& activity = std::get<nds::ActivityCommand>(*command).activity;
    CHECK(activity.details == "Undone");
    CHECK(activity.state == "Weezer");
    CHECK(activity.largeImage == "https://img/a.jpg");
    CHECK(activity.startMs == 1000);
    CHECK(activity.endMs == 5000);
    CHECK(activity.smallImage.empty());
}

void testIgnoresBadFieldTypes()
{
    const auto command = parse(R"({"op":"activity","activity":{"details":42,"startMs":-5,"endMs":"soon"}})");
    CHECK(command.has_value());
    if (command) {
        const auto& activity = std::get<nds::ActivityCommand>(*command).activity;
        CHECK(activity.details.empty());
        CHECK(activity.startMs == 0);
        CHECK(activity.endMs == 0);
    }
}

void testRejectsBadCommands()
{
    CHECK(parseError("not json") == "command is not a JSON object");
    CHECK(parseError("[1,2]") == "command is not a JSON object");
    CHECK(parseError(R"({"op":"teleport"})") == "unknown command");
    CHECK(parseError(R"({"op":"connect"})") == "connect command without token");
    CHECK(parseError(R"({"op":"activity"})") == "activity command without activity");
}

void testEventsAreValidJsonLines()
{
    CHECK(nds::readyEvent() == R"({"event":"ready"})");
    CHECK(nds::errorEvent("boom") == R"({"error":"boom","event":"error"})");
    const std::string log = nds::logEvent("warning", "line\nbreak");
    CHECK(log.find('\n') == std::string::npos);
}

// Runs fn with a LineReader reading from a pipe that holds input.
void withPipe(const std::string& input, bool closeWriter, const std::function<void(nds::LineReader&)>& fn)
{
    int fds[2];
    CHECK(pipe(fds) == 0);
    CHECK(write(fds[1], input.data(), input.size()) == static_cast<ssize_t>(input.size()));
    if (closeWriter) {
        close(fds[1]);
    }
    nds::LineReader reader(fds[0], 16);
    fn(reader);
    close(fds[0]);
    if (!closeWriter) {
        close(fds[1]);
    }
}

void testReadsLines()
{
    withPipe("one\ntwo\npart", false, [](nds::LineReader& reader) {
        reader.poll(std::chrono::milliseconds(100));
        CHECK(reader.nextLine() == std::optional<std::string>("one"));
        CHECK(reader.nextLine() == std::optional<std::string>("two"));
        CHECK(!reader.nextLine().has_value());
        CHECK(!reader.finished());
    });
}

void testDropsOverlongLines()
{
    withPipe(std::string(40, 'x') + "\nshort\n", true, [](nds::LineReader& reader) {
        for (int i = 0; i < 5; ++i) {
            reader.poll(std::chrono::milliseconds(50));
        }
        CHECK(reader.nextLine() == std::optional<std::string>("short"));
        CHECK(reader.takeDroppedLines() == 1);
        CHECK(reader.finished());
    });
}

void testFinishesOnEndOfInput()
{
    withPipe("last\n", true, [](nds::LineReader& reader) {
        reader.poll(std::chrono::milliseconds(50));
        reader.poll(std::chrono::milliseconds(50));
        CHECK(reader.nextLine() == std::optional<std::string>("last"));
        CHECK(reader.finished());
    });
}

} // namespace

int main()
{
    testParsesEveryCommand();
    testParsesActivity();
    testIgnoresBadFieldTypes();
    testRejectsBadCommands();
    testEventsAreValidJsonLines();
    testReadsLines();
    testDropsOverlongLines();
    testFinishesOnEndOfInput();

    if (failures > 0) {
        std::fprintf(stderr, "%d check(s) failed\n", failures);
        return 1;
    }
    std::puts("all worker tests passed");
    return 0;
}
