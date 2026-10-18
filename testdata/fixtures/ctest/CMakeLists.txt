cmake_minimum_required(VERSION 3.20)
project(mcp_ctest_fixture NONE)

enable_testing()

add_test(NAME pass COMMAND ${CMAKE_COMMAND} -E echo "fixture-pass")
add_test(NAME fail COMMAND ${CMAKE_COMMAND} -E false)
add_test(NAME disabled COMMAND ${CMAKE_COMMAND} -E echo "fixture-disabled")
set_tests_properties(disabled PROPERTIES DISABLED TRUE)
add_test(NAME timeout COMMAND ${CMAKE_COMMAND} -E sleep 2)

add_test(NAME fixture_setup COMMAND ${CMAKE_COMMAND} -E echo "fixture-setup")
set_tests_properties(fixture_setup PROPERTIES FIXTURES_SETUP fixture_db)

add_test(NAME requires_fixture COMMAND ${CMAKE_COMMAND} -E echo "fixture-main")
set_tests_properties(requires_fixture PROPERTIES FIXTURES_REQUIRED fixture_db)

add_test(NAME property_timeout COMMAND ${CMAKE_COMMAND} -E sleep 2)
set_tests_properties(property_timeout PROPERTIES TIMEOUT 30)
