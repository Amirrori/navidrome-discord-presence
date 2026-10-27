#include "presence_session.h"

#include <utility>

namespace nds {
namespace {

// The SDK keeps looking for a local Discord app over RPC. A server never has
// one, so these messages are expected and not worth forwarding.
bool isLocalClientNoise(const std::string& message)
{
    return message.find("rpc_manager") != std::string::npos;
}

std::optional<std::string> optionalText(const std::string& value)
{
    if (value.empty()) {
        return std::nullopt;
    }
    return value;
}

discordpp::Activity buildActivity(const Activity& source)
{
    discordpp::Activity activity;
    activity.SetType(discordpp::ActivityTypes::Listening);
    activity.SetStatusDisplayType(discordpp::StatusDisplayTypes::Details);
    activity.SetDetails(source.details);
    activity.SetDetailsUrl(optionalText(source.detailsUrl));
    activity.SetState(source.state);
    activity.SetStateUrl(optionalText(source.stateUrl));

    discordpp::ActivityAssets assets;
    assets.SetLargeImage(optionalText(source.largeImage));
    assets.SetLargeText(optionalText(source.largeText));
    assets.SetLargeUrl(optionalText(source.largeUrl));
    assets.SetSmallImage(optionalText(source.smallImage));
    assets.SetSmallText(optionalText(source.smallText));
    activity.SetAssets(assets);

    if (source.startMs != 0) {
        discordpp::ActivityTimestamps timestamps;
        timestamps.SetStart(source.startMs);
        if (source.endMs > source.startMs) {
            timestamps.SetEnd(source.endMs);
        }
        activity.SetTimestamps(timestamps);
    }
    return activity;
}

} // namespace

PresenceSession::PresenceSession(std::uint64_t applicationId, std::function<void(const std::string&)> emit)
    : emit_(std::move(emit))
{
    client_.AddLogCallback(
        [this](std::string message, discordpp::LoggingSeverity severity) {
            if (severity < discordpp::LoggingSeverity::Warning || isLocalClientNoise(message)) {
                return;
            }
            const char* level = severity >= discordpp::LoggingSeverity::Error ? "error" : "warning";
            emit_(logEvent(level, message));
        },
        discordpp::LoggingSeverity::Warning);
    client_.SetApplicationId(applicationId);
    client_.SetStatusChangedCallback(
        [this](discordpp::Client::Status status, discordpp::Client::Error error, int32_t detail) {
            onStatusChanged(status, error, detail);
        });
}

void PresenceSession::connect(const std::string& accessToken)
{
    client_.UpdateToken(discordpp::AuthorizationTokenType::Bearer, accessToken, [this](discordpp::ClientResult result) {
        if (!result.Successful()) {
            emit_(errorEvent("token rejected: " + result.Error()));
            return;
        }
        client_.Connect();
    });
}

void PresenceSession::updateToken(const std::string& accessToken)
{
    client_.UpdateToken(discordpp::AuthorizationTokenType::Bearer, accessToken, [this](discordpp::ClientResult result) {
        if (!result.Successful()) {
            emit_(errorEvent("token update rejected: " + result.Error()));
        }
    });
}

void PresenceSession::setActivity(const Activity& activity)
{
    current_ = activity;
    if (ready_) {
        publish(activity);
    }
}

void PresenceSession::clear()
{
    current_.reset();
    if (ready_) {
        client_.ClearRichPresence();
    }
}

void PresenceSession::disconnect()
{
    clear();
    client_.Disconnect();
}

void PresenceSession::onStatusChanged(discordpp::Client::Status status, discordpp::Client::Error error, int32_t detail)
{
    const bool wasReady = ready_;
    ready_ = status == discordpp::Client::Status::Ready;

    if (ready_ && !wasReady) {
        emit_(readyEvent());
        if (current_) {
            publish(*current_);
        }
    } else if (!ready_ && wasReady) {
        emit_(disconnectedEvent(discordpp::Client::ErrorToString(error) + " (" + std::to_string(detail) + ")"));
    } else if (error != discordpp::Client::Error::None) {
        emit_(errorEvent("connection failed: " + discordpp::Client::ErrorToString(error) + " (" +
                         std::to_string(detail) + ")"));
    }
}

void PresenceSession::publish(const Activity& activity)
{
    client_.UpdateRichPresence(buildActivity(activity), [this](discordpp::ClientResult result) {
        if (!result.Successful()) {
            emit_(errorEvent("status update failed: " + result.Error()));
        }
    });
}

} // namespace nds
