// Wraps one user's Discord Social SDK client: authentication with a bearer
// token, the connection state and the rich presence.
#pragma once

#include "protocol.h"

#include <cstdint>
#include <functional>
#include <optional>
#include <string>

#include "discordpp.h"

namespace nds {

class PresenceSession {
public:
    // emit receives event lines for the bridge server.
    PresenceSession(std::uint64_t applicationId, std::function<void(const std::string&)> emit);

    void connect(const std::string& accessToken);
    void updateToken(const std::string& accessToken);
    void setActivity(const Activity& activity);
    void clear();
    void disconnect();

private:
    void onStatusChanged(discordpp::Client::Status status, discordpp::Client::Error error, int32_t detail);
    void publish(const Activity& activity);

    discordpp::Client client_;
    std::function<void(const std::string&)> emit_;
    bool ready_ = false;
    // The status that should be shown; published again after every reconnect.
    std::optional<Activity> current_;
};

} // namespace nds
