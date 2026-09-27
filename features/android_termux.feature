Feature: Coddy on Android under Termux
  The Android release is the Go binary built with GOOS=android: a position
  independent executable that names Android's system linker as its
  interpreter. Termux runs it directly where Android lets the app execute the
  files in its data directory, and as `/system/bin/linker64 <path>` where it
  does not, which is every device running a Termux that targets Android 10 or
  later, such as the Google Play build. Coddy then does for the programs it
  starts what termux-exec does for Termux's own: it runs them through the same
  linker, and it finds the interpreters that scripts name by their Linux paths
  in the Termux prefix. It resolves hostnames with the nameservers Termux
  configures, because Android has no /etc/resolv.conf for the Go resolver.

  Scenario: Reading its own arguments when the system linker started Coddy
    Given the system linker started Coddy with the arguments "serve --daemon"
    When Coddy reads its command line
    Then it sees the arguments "serve --daemon"
    And it knows the path of its own binary

  Scenario: Running a shell command when Termux starts programs through the linker
    Given the system linker started Coddy
    And bash is installed in the Termux prefix
    When Coddy starts bash for a command
    Then the command runs the system linker on bash
    And bash learns its own path from TERMUX_EXEC__PROC_SELF_EXE

  Scenario: Running a Node script whose shebang names /usr/bin/env
    Given the system linker started Coddy
    And npx is installed in the Termux prefix with the shebang "#!/usr/bin/env node"
    When Coddy starts npx for an MCP server
    Then the command runs the system linker on the env of the Termux prefix
    And env receives node and the path of the script

  Scenario: Running the same script where Termux executes its files directly
    Given the kernel started Coddy directly
    And npx is installed in the Termux prefix with the shebang "#!/usr/bin/env node"
    When Coddy starts npx for an MCP server
    Then the command runs the env of the Termux prefix directly
    And env receives node and the path of the script

  Scenario: Resolving hostnames with the nameservers Termux configures
    Given the Termux resolv.conf names the nameserver "1.1.1.1"
    When the Go resolver queries its default nameserver
    Then the query goes to "1.1.1.1:53"
