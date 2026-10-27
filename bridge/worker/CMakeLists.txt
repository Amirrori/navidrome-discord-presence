cmake_minimum_required(VERSION 3.20)
project(nds-worker LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

option(NDS_BUILD_WORKER "Build nds-worker (needs the Discord Social SDK)" ON)
option(NDS_BUILD_TESTS "Build the unit tests (they don't need the SDK)" OFF)
option(NDS_WARNINGS_AS_ERRORS "Treat compiler warnings as errors" ON)

set(NDS_WARNINGS -Wall -Wextra -Wpedantic -Wshadow -Wconversion)
if(NDS_WARNINGS_AS_ERRORS)
  list(APPEND NDS_WARNINGS -Werror)
endif()

include(FetchContent)
FetchContent_Declare(
  nlohmann_json
  URL https://github.com/nlohmann/json/releases/download/v3.11.3/json.tar.xz
  URL_HASH SHA256=d6c65aca6b1ed68e7a182f4757257b107ae403032760ed6ef121c9d55e81757d
  DOWNLOAD_EXTRACT_TIMESTAMP TRUE
)
FetchContent_MakeAvailable(nlohmann_json)

# The parts that don't depend on the SDK: the line protocol and stdin reader.
add_library(nds_core STATIC src/protocol.cpp src/line_reader.cpp)
target_include_directories(nds_core PUBLIC src)
target_link_libraries(nds_core PRIVATE nlohmann_json::nlohmann_json)
target_compile_options(nds_core PRIVATE ${NDS_WARNINGS} "-ffile-prefix-map=${CMAKE_SOURCE_DIR}=.")

if(NDS_BUILD_WORKER)
  # The Discord Social SDK is proprietary and not part of this repository.
  # Download the C++ SDK from the Discord developer portal and point this at
  # the unpacked discord_social_sdk directory.
  set(DISCORD_SOCIAL_SDK_DIR "${CMAKE_CURRENT_SOURCE_DIR}/../../third_party/discord_social_sdk"
      CACHE PATH "Path to the unpacked discord_social_sdk directory")

  find_path(DISCORD_SOCIAL_SDK_INCLUDE_DIR discordpp.h
            PATHS "${DISCORD_SOCIAL_SDK_DIR}/include" NO_DEFAULT_PATH REQUIRED)
  find_library(DISCORD_SOCIAL_SDK_LIBRARY discord_partner_sdk
               PATHS "${DISCORD_SOCIAL_SDK_DIR}/lib/release" NO_DEFAULT_PATH REQUIRED)

  add_executable(nds-worker src/main.cpp src/presence_session.cpp)
  target_include_directories(nds-worker SYSTEM PRIVATE "${DISCORD_SOCIAL_SDK_INCLUDE_DIR}")
  target_link_libraries(nds-worker PRIVATE nds_core "${DISCORD_SOCIAL_SDK_LIBRARY}")
  target_compile_options(nds-worker PRIVATE ${NDS_WARNINGS}
    "-ffile-prefix-map=${CMAKE_SOURCE_DIR}=."
    "-ffile-prefix-map=${CMAKE_BINARY_DIR}=build"
    "-ffile-prefix-map=${DISCORD_SOCIAL_SDK_DIR}=discord_social_sdk")

  # Look for the SDK library only next to the binary, and keep machine-specific
  # paths out of it.
  set_target_properties(nds-worker PROPERTIES
    SKIP_BUILD_RPATH TRUE
    BUILD_WITH_INSTALL_RPATH TRUE
    INSTALL_RPATH "$ORIGIN")
  add_custom_command(TARGET nds-worker POST_BUILD
    COMMAND "${CMAKE_COMMAND}" -E copy_if_different "${DISCORD_SOCIAL_SDK_LIBRARY}" "$<TARGET_FILE_DIR:nds-worker>"
    COMMENT "Copying the Discord Social SDK library next to nds-worker")
  # The SDK library references system libraries without being linked to them.
  target_link_options(nds-worker PRIVATE "LINKER:--allow-shlib-undefined")

  install(TARGETS nds-worker RUNTIME DESTINATION .)
  install(FILES "${DISCORD_SOCIAL_SDK_LIBRARY}" DESTINATION .)
endif()

if(NDS_BUILD_TESTS)
  enable_testing()
  add_executable(nds-worker-tests tests/worker_tests.cpp)
  target_link_libraries(nds-worker-tests PRIVATE nds_core)
  target_compile_options(nds-worker-tests PRIVATE ${NDS_WARNINGS})
  add_test(NAME nds-worker-tests COMMAND nds-worker-tests)
endif()
