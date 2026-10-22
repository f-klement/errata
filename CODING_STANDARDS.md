# Coding standards

Rules a reviewer applies to a change. They are judgement calls: anything a tool can check is a
check in `make check` instead, and is not repeated here.

## Tests run code the way production runs it

A test of a script calls it exactly as its caller does: the same working directory, the same
relative or absolute paths, the same environment variables. For a CI script that caller is
`.gitlab-ci.yml`, so read the job before reading the test. A test that passes an absolute path
where CI passes a relative one tests a different program: that is how `ci/autopatch.sh` passed
every test and stopped on its first night.

## Every guard has a witness

A check, a refusal or a test that guards a rule is shown failing on a planted break of that rule
before it is trusted: change the input it should refuse, watch it refuse, restore the input. Name
the planted break in the commit or the test. A guard nobody has seen fail may never fail, and a
suite of such guards goes green on nothing.

A refusal is tested for its refusal, not for any failure: assert the message (`REFUSED`, the named
reason), so a crash for an unrelated reason does not pass as the guard working.
