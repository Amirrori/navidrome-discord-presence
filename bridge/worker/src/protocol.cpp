#include "protocol.h"

#include <nlohmann/json.hpp>

namespace nds {
namespace {

using nlohmann::json;

std::string stringField(const json& object, const char* key)
{
    const auto it = object.find(key);
    return it != object.end() && it->is_string() ? it->get<std::string>() : std::string();
}

std::uint64_t timeField(const json& object, const char* key)
{
    const auto it = object.find(key);
    return it != object.end() && it->is_number_unsigned() ? it->get<std::uint64_t>() : 0;
}

Activity parseActivity(const json& object)
{
    Activity activity;
    activity.details = stringField(object, "details");
    activity.detailsUrl = stringField(object, "detailsUrl");
    activity.state = stringField(object, "state");
    activity.stateUrl = stringField(object, "stateUrl");
    activity.largeImage = stringField(object, "largeImage");
    activity.largeText = stringField(object, "largeText");
    activity.largeUrl = stringField(object, "largeUrl");
    activity.smallImage = stringField(object, "smallImage");
    activity.smallText = stringField(object, "smallText");
    activity.startMs = timeField(object, "startMs");
    activity.endMs = timeField(object, "endMs");
    return activity;
}

std::string event(json object)
{
    return object.dump(-1, ' ', false, json::error_handler_t::replace);
}

} // namespace

std::optional<Command> parseCommand(const std::string& line, std::string& error)
{
    const json object = json::parse(line, nullptr, false);
    if (object.is_discarded() || !object.is_object()) {
        error = "command is not a JSON object";
        return std::nullopt;
    }

    const std::string op = stringField(object, "op");
    if (op == "connect" || op == "token") {
        std::string token = stringField(object, "token");
        if (token.empty()) {
            error = op + " command without token";
            return std::nullopt;
        }
        if (op == "connect") {
            return ConnectCommand{std::move(token)};
        }
        return TokenCommand{std::move(token)};
    }
    if (op == "activity") {
        const auto it = object.find("activity");
        if (it == object.end() || !it->is_object()) {
            error = "activity command without activity";
            return std::nullopt;
        }
        return ActivityCommand{parseActivity(*it)};
    }
    if (op == "clear") {
        return ClearCommand{};
    }
    if (op == "shutdown") {
        return ShutdownCommand{};
    }

    error = "unknown command";
    return std::nullopt;
}

std::string readyEvent()
{
    return event({{"event", "ready"}});
}

std::string disconnectedEvent(const std::string& error)
{
    return event({{"event", "disconnected"}, {"error", error}});
}

std::string errorEvent(const std::string& error)
{
    return event({{"event", "error"}, {"error", error}});
}

std::string logEvent(const std::string& level, const std::string& message)
{
    return event({{"event", "log"}, {"level", level}, {"message", message}});
}

} // namespace nds
