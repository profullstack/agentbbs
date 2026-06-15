// Package brand holds AgentBBS's terminal branding — the ASCII logo shown
// at the top of the join@ onboarding and the ssh <name>@ hub prompts.
// Scaled down from the Profullstack </> mark.
package brand

import "strings"

// logo is the ASCII rendition of the </> brand mark (UTF-8 block glyphs).
const logo = `
                            ▓████▓▒
                           ▒█████▓
          +▒▓             +▓████▓+   ▒+
      :+▒▓▓▓▓            :▓██▓█▓+    ▒▓▓▓▒+
   +▒▒▓▓▓▓▓▓▓            ▓▓▓▓▓▓+     ▒▓▓▓▓▓▒▒+
:+▒▓▓▓▓▓▓▓▒+            ▒▓▓▓▓▓▒       +▒▓▓▓▓▓▓▒▒:
▒▓▓▒▓▒▒▒:              +▓▓▓▓▓▓           :▒▒▒▒▒▒▒▒
▒▒▒▒++                 ▒▓▓▓▓▓+              ++▒▒▒▒
▒▒+                   +▓▓▓▓▓▒                  ++▒
▒▒▒▒+                 ▒▓▓▓▓▒                  +▒▒▒
▒▒▒▒▒▒▒+             ▒▓▓▓▓▒               +▒▒▒▒▒▒▒
+▒▒▒▒▒▒▒▒▒+         +▓▓▓▓▓:            +▒▒▒▒▒▒▒▒++
`

// Logo returns the plain (unstyled) multi-line ASCII brand banner, without
// leading or trailing blank lines. The hub/join@ flows apply their own color.
func Logo() string { return strings.Trim(logo, "\n") }
