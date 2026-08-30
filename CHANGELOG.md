# Changelog

## [0.7.0](https://github.com/dreulavelle/mcpd/compare/v0.6.1...v0.7.0) (2026-08-30)


### Features

* **approvals:** a window that stops the asking, and closes on its own ([618acf7](https://github.com/dreulavelle/mcpd/commit/618acf721ce4dc15ad331775b59ba21b1ca69b26))
* **approvals:** a window that stops the asking, and closes on its own ([06df3c4](https://github.com/dreulavelle/mcpd/commit/06df3c4687abb2d4a664a452dafb9c313cf9d19c))
* **backup:** take a whole instance as one encrypted file, and put it back ([0bc381d](https://github.com/dreulavelle/mcpd/commit/0bc381d5c12a4c33914c3f2671292a465392285c))
* **backup:** take a whole instance as one encrypted file, and put it back ([5617a9b](https://github.com/dreulavelle/mcpd/commit/5617a9bb8b1af3925c9492465f018615cf403616))
* **mcpservers:** re-ask each remote server what it offers, on a timer ([312e91f](https://github.com/dreulavelle/mcpd/commit/312e91f825bd026f76128d7cc30103917ea6ad4d))
* **mcpservers:** re-ask each remote server what it offers, on a timer ([d49b1e9](https://github.com/dreulavelle/mcpd/commit/d49b1e900fca92313cbb980cfcb0d041cee2b654))
* **notify:** tell an operator what happened, and never ask them to approve it ([ad86393](https://github.com/dreulavelle/mcpd/commit/ad86393f85fe1b5d1be877f5e3d68b896f4a2e2b))
* **notify:** tell an operator what happened, and never ask them to approve it ([3cfbca3](https://github.com/dreulavelle/mcpd/commit/3cfbca3d85b1514ebe67c7b75388cab6c1814757))
* **observability:** record who called what, and show it ([4150e98](https://github.com/dreulavelle/mcpd/commit/4150e9845a1860cefd4ac120f97ee4bce29255de))
* **observability:** record who called what, and show it ([5e6862b](https://github.com/dreulavelle/mcpd/commit/5e6862b62af98b18fc08afda019377015e0bec58))
* reach MCP servers whose documents declare no credential, and import client configs ([#99](https://github.com/dreulavelle/mcpd/issues/99)) ([478359a](https://github.com/dreulavelle/mcpd/commit/478359a46059d16840434839bfa5afcae27d593c))
* **registry:** a catalogue of your own, fetched from wherever you keep it ([42c7aa2](https://github.com/dreulavelle/mcpd/commit/42c7aa2c5eccdc9055f8f2566413c7c485f97462))
* **registry:** a catalogue of your own, fetched from wherever you keep it ([46e2b7a](https://github.com/dreulavelle/mcpd/commit/46e2b7a5c151e7fed183b073b7e53c8f91c9567d))


### Fixes

* **dashboard:** let the Settings tab be the only way into users and groups ([e12233a](https://github.com/dreulavelle/mcpd/commit/e12233a72219241bc5dc381c6900cca71b508a02))
* three bugs a review of the last four features turned up, and their docs ([cf6ed42](https://github.com/dreulavelle/mcpd/commit/cf6ed4232c275de36f5a2251ec495feb9a083d19))
* three bugs a review of the last four features turned up, and their docs ([12632ea](https://github.com/dreulavelle/mcpd/commit/12632ea0901825ff493fde54e868a16b187a5131))

## [0.6.1](https://github.com/dreulavelle/mcpd/compare/v0.6.0...v0.6.1) (2026-08-27)


### Fixes

* **deploy:** make ./data before the first start, or the container cannot write ([#97](https://github.com/dreulavelle/mcpd/issues/97)) ([9a0493f](https://github.com/dreulavelle/mcpd/commit/9a0493f50c40a00d9c2a58fa3302cc12ff719336))

## [0.6.0](https://github.com/dreulavelle/mcpd/compare/v0.5.0...v0.6.0) (2026-08-27)


### Features

* **deploy:** run a published release without building it first ([#94](https://github.com/dreulavelle/mcpd/issues/94)) ([ebd0c41](https://github.com/dreulavelle/mcpd/commit/ebd0c4111675ad5f2681490841d4b0d66b6a8f32))
* **extremecloudiq:** read an Extreme Networks estate, and say what is wrong with it ([#94](https://github.com/dreulavelle/mcpd/issues/94)) ([ebd0c41](https://github.com/dreulavelle/mcpd/commit/ebd0c4111675ad5f2681490841d4b0d66b6a8f32))


### Fixes

* **tunnel:** serialise a tunnel's lifecycle so two settings changes cannot race ([#95](https://github.com/dreulavelle/mcpd/issues/95)) ([04845fd](https://github.com/dreulavelle/mcpd/commit/04845fd83107fd0630014762a9c25b1768811c62))


### Documentation

* **readme:** lead with what mcpd does for somebody, not how it is built ([#94](https://github.com/dreulavelle/mcpd/issues/94)) ([ebd0c41](https://github.com/dreulavelle/mcpd/commit/ebd0c4111675ad5f2681490841d4b0d66b6a8f32))

## [0.5.0](https://github.com/dreulavelle/mcpd/compare/v0.4.1...v0.5.0) (2026-08-27)


### Features

* **chatgpt:** connect several ChatGPT accounts, each with its own identity ([#91](https://github.com/dreulavelle/mcpd/issues/91)) ([d8f0f91](https://github.com/dreulavelle/mcpd/commit/d8f0f91eb65a8c6f7135dd20c9f396e9b84f71e7))
* **marketplace:** hold the catalogues rather than proxying them ([#92](https://github.com/dreulavelle/mcpd/issues/92)) ([4ce4ac1](https://github.com/dreulavelle/mcpd/commit/4ce4ac1b19ccfa38ecf91b73aba951e66f7e28c4))
* **settings:** one navigation, and groups that say where they live ([#93](https://github.com/dreulavelle/mcpd/issues/93)) ([01af420](https://github.com/dreulavelle/mcpd/commit/01af4205f7b498edac0906ee16d9a2ab4cc57fda))


### Fixes

* **dashboard:** let a dialog taller than the window be scrolled ([#90](https://github.com/dreulavelle/mcpd/issues/90)) ([77f3cfd](https://github.com/dreulavelle/mcpd/commit/77f3cfd498821fde8db0decd0dba8b28a0695ca9))
* **performance:** report what was measured, not where a boundary fell ([#88](https://github.com/dreulavelle/mcpd/issues/88)) ([08b849b](https://github.com/dreulavelle/mcpd/commit/08b849b9771965cc33b1ce20bfabde37ab5bedb5))

## [0.4.1](https://github.com/dreulavelle/mcpd/compare/v0.4.0...v0.4.1) (2026-08-27)


### Fixes

* **dashboard:** render release notes, and move the version off the account ([#86](https://github.com/dreulavelle/mcpd/issues/86)) ([b991a9a](https://github.com/dreulavelle/mcpd/commit/b991a9a7867e1be712eec036eaadea5f60170590))

## [0.4.0](https://github.com/dreulavelle/mcpd/compare/v0.3.0...v0.4.0) (2026-08-27)


### Features

* measure what a call costs, and stop paying twice ([#84](https://github.com/dreulavelle/mcpd/issues/84)) ([ec00c61](https://github.com/dreulavelle/mcpd/commit/ec00c618b65e5d59507777230b49eccd24cb2950))

## [0.3.0](https://github.com/dreulavelle/mcpd/compare/v0.2.0...v0.3.0) (2026-08-27)


### Features

* **graylog:** read a log platform, and hold every plugin to one standard ([#80](https://github.com/dreulavelle/mcpd/issues/80)) ([4b07b28](https://github.com/dreulavelle/mcpd/commit/4b07b2835f0b7c72baf62d7d7ecc59942af35151))
* **trust:** trust your own certificate authority, and say which instance is which ([#82](https://github.com/dreulavelle/mcpd/issues/82)) ([d1be8e8](https://github.com/dreulavelle/mcpd/commit/d1be8e8934ed09ae41d517dfe31cb7a4ee8e0521))


### Fixes

* **version:** the binary carries its own version, and there is no "dev" ([#83](https://github.com/dreulavelle/mcpd/issues/83)) ([456403b](https://github.com/dreulavelle/mcpd/commit/456403bb182e5949552e72df09a2303090229760))

## [0.2.0](https://github.com/dreulavelle/mcpd/compare/v0.1.0...v0.2.0) (2026-08-26)


### Features

* .env support, mcpd -init, and OpenAI Secure MCP Tunnel in compose ([a28cd01](https://github.com/dreulavelle/mcpd/commit/a28cd01489f5d81986baaff45242a89936054cbe))
* a debug switch for the tunnel, and compare Debug in SameAs ([d584c82](https://github.com/dreulavelle/mcpd/commit/d584c8265377c98409a095d4ac5353bc8014a9c1))
* a new instance asks for its first account, and there are two roles ([#5](https://github.com/dreulavelle/mcpd/issues/5)) ([25262d8](https://github.com/dreulavelle/mcpd/commit/25262d847a8bd5fd1a8fdf8ee9d40ab3d9d347a5))
* a plugin's settings are declared by the plugin and edited in the dashboard ([#23](https://github.com/dreulavelle/mcpd/issues/23)) ([9719b36](https://github.com/dreulavelle/mcpd/commit/9719b3633f88f4b4a60d56666d550990c880bc6a))
* add and remove plugin instances from the dashboard ([#25](https://github.com/dreulavelle/mcpd/issues/25)) ([ad28027](https://github.com/dreulavelle/mcpd/commit/ad280277e3712f8d9f27a71f8bec7cc62bd6dc05))
* **admin:** operator dashboard on its own listener, port 80 by default ([3f8819c](https://github.com/dreulavelle/mcpd/commit/3f8819c718fc24f8875585209c7de0ea27aa7b4b))
* aggregate /mcp endpoint, and run under Docker ([26929eb](https://github.com/dreulavelle/mcpd/commit/26929eb112387383ffc5b4268ed72bd586a4afc2))
* **approval:** ask in the conversation instead of the dashboard ([19ea974](https://github.com/dreulavelle/mcpd/commit/19ea974a4da7305124abe7956530141c6c7f7058))
* **approvals:** a change nobody wrote a rule about goes ahead ([#65](https://github.com/dreulavelle/mcpd/issues/65)) ([62021da](https://github.com/dreulavelle/mcpd/commit/62021da62354bcbf2f2069a658b3fbcc2d87bdd6))
* **auth:** built-in OAuth 2.1 authorization server ([9b9bbf8](https://github.com/dreulavelle/mcpd/commit/9b9bbf8b508cf77ec5912f62820006b4acc475c5))
* **cnmaestro:** authenticate, and read networks and devices ([#17](https://github.com/dreulavelle/mcpd/issues/17)) ([90c3aa8](https://github.com/dreulavelle/mcpd/commit/90c3aa84aa6cc04d2617e965260bc64c612b4de2))
* **cnmaestro:** list managed accounts, and get managed_account right ([#18](https://github.com/dreulavelle/mcpd/issues/18)) ([ece0c8e](https://github.com/dreulavelle/mcpd/commit/ece0c8e282d63ad5344d561523200fe91323e843))
* **cnmaestro:** reference plugin with read tools and typed mutations ([c2b2b0e](https://github.com/dreulavelle/mcpd/commit/c2b2b0ebe5fbde7c1edcde7f4ae63e9ea472e76c))
* **cnmaestro:** thirteen more read tools, covering what the API actually offers ([#30](https://github.com/dreulavelle/mcpd/issues/30)) ([3d4a465](https://github.com/dreulavelle/mcpd/commit/3d4a4654960d9d4594720a294f9b2a45927e54db))
* **config:** five lines in a file, and everything else where it is recorded ([#54](https://github.com/dreulavelle/mcpd/issues/54)) ([edf109d](https://github.com/dreulavelle/mcpd/commit/edf109dd3bfd3ae652cbe736ea5d787659633092))
* consistent database backups with mcpd -backup ([037bc2d](https://github.com/dreulavelle/mcpd/commit/037bc2dd598e6cb61a92921618b2752e649d77c0))
* **dashboard:** the policy has a page, and an auto-approval says nobody was asked ([#41](https://github.com/dreulavelle/mcpd/issues/41)) ([79a64e3](https://github.com/dreulavelle/mcpd/commit/79a64e33bc212f1d56bbfca41be43f8db63e55e1))
* expose the tunnel client's own diagnostics ([f344be5](https://github.com/dreulavelle/mcpd/commit/f344be59129e427cfdd7cfdada7c6b140b7a9931))
* history retention and clearing; drop the second-approver rule ([4d581d7](https://github.com/dreulavelle/mcpd/commit/4d581d7372958511cc6a7f063a291a0cd103dd70))
* link to where the last manual step happens ([#13](https://github.com/dreulavelle/mcpd/issues/13)) ([13589f4](https://github.com/dreulavelle/mcpd/commit/13589f444da5233ab46413e0e1b4ff4ae7ec807c))
* **logs:** watch what this host is doing, from the dashboard ([#61](https://github.com/dreulavelle/mcpd/issues/61)) ([1d85120](https://github.com/dreulavelle/mcpd/commit/1d85120693e85c90837215182263937b32861d26))
* MCP host, per-plugin auth scoping, and Docker setup ([b73afc8](https://github.com/dreulavelle/mcpd/commit/b73afc8b8014afdd9be064ab83a863a2237e4dde))
* **observability:** crash reporting that does not carry the customer with it ([#74](https://github.com/dreulavelle/mcpd/issues/74)) ([fbae64d](https://github.com/dreulavelle/mcpd/commit/fbae64d7fd26ada138f4236990390e5a40d2eed9))
* **observability:** logs a support call can actually use ([#75](https://github.com/dreulavelle/mcpd/issues/75)) ([4545bcb](https://github.com/dreulavelle/mcpd/commit/4545bcb129f551bf78d570a621993dbe3a56742e))
* **observium:** read an SNMP estate without pretending to have its history ([#67](https://github.com/dreulavelle/mcpd/issues/67)) ([2885e56](https://github.com/dreulavelle/mcpd/commit/2885e56a01e5f3665a17246624de8a3f41f38eb0))
* **observium:** read Community Edition, where there is no API to read ([#69](https://github.com/dreulavelle/mcpd/issues/69)) ([5b3d129](https://github.com/dreulavelle/mcpd/commit/5b3d12925a6e6e23658085e2060f0f10d832ecf9))
* **observium:** use the API we are actually pointed at ([#77](https://github.com/dreulavelle/mcpd/issues/77)) ([e231a3c](https://github.com/dreulavelle/mcpd/commit/e231a3c42ae26ad69ab350d277656fd45f122472))
* one integration can be configured more than once ([#21](https://github.com/dreulavelle/mcpd/issues/21)) ([8416550](https://github.com/dreulavelle/mcpd/commit/8416550e1e9ee7be56b67d6689972a75209d00c8))
* one tunnel per plugin, so a connector can serve one system ([4e6f4aa](https://github.com/dreulavelle/mcpd/commit/4e6f4aa00780f7726f5efac4f87f9759bda6ecd7))
* operations domain and SQLite storage foundation ([386dcb7](https://github.com/dreulavelle/mcpd/commit/386dcb74dcaa78f7991e7ba1e935aa026692633a))
* **operations:** approval service, executor, verifier, reaper, event bus ([b12550f](https://github.com/dreulavelle/mcpd/commit/b12550f768a337e41d3048d8576ba499657e4473))
* people sign in with an email and a password ([#2](https://github.com/dreulavelle/mcpd/issues/2)) ([f59f5f2](https://github.com/dreulavelle/mcpd/commit/f59f5f210554852667ddceb7835cbecdfd5b8377))
* pick the workspace when making a tunnel ([#12](https://github.com/dreulavelle/mcpd/issues/12)) ([2977d64](https://github.com/dreulavelle/mcpd/commit/2977d64edbb4846d595c4f760702abf61d56dc94))
* **plugins:** resources, prompts, per-tool capability and rate limits ([#24](https://github.com/dreulavelle/mcpd/issues/24)) ([b4ded7e](https://github.com/dreulavelle/mcpd/commit/b4ded7ed7abdd94806b6aa0c1d140a663a5cd34e))
* **registry:** read every published server.json format, and browse Docker's catalogue too ([#37](https://github.com/dreulavelle/mcpd/issues/37)) ([c95a7d4](https://github.com/dreulavelle/mcpd/commit/c95a7d45b9b853aa01e3ed29d72b9c6376de3177))
* **registry:** two more catalogues, and one reader for the two that agree ([#39](https://github.com/dreulavelle/mcpd/issues/39)) ([3c24794](https://github.com/dreulavelle/mcpd/commit/3c2479479aa79d5b2274415b1f27c2733beed843))
* scope new tunnels to a workspace, and stop asking for what we know ([#11](https://github.com/dreulavelle/mcpd/issues/11)) ([1fae540](https://github.com/dreulavelle/mcpd/commit/1fae540ad071bca1b5aea24fc1a90db856d2fc06))
* **sdk:** out-of-process plugins and the Go SDK for writing them ([c8a5443](https://github.com/dreulavelle/mcpd/commit/c8a54436ad7313e8c75507097b312bd7f2e4c7e6))
* **settings:** a catalog, so a plugin can declare its own settings ([#22](https://github.com/dreulavelle/mcpd/issues/22)) ([fbbe2e0](https://github.com/dreulavelle/mcpd/commit/fbbe2e0e9182b74b77d7c28ebeb90b62a957c115))
* **settings:** a field can depend on the answer to another one ([#68](https://github.com/dreulavelle/mcpd/issues/68)) ([6abd675](https://github.com/dreulavelle/mcpd/commit/6abd6750907f68a3c0cb2c10d267f1a039ac7031))
* **settings:** an enum can name its values in the words an operator uses ([#71](https://github.com/dreulavelle/mcpd/issues/71)) ([d67b491](https://github.com/dreulavelle/mcpd/commit/d67b4919d63f8f1476a9cc40753b3eed00b0126c))
* **settings:** manage configuration from the dashboard ([4e8dd3a](https://github.com/dreulavelle/mcpd/commit/4e8dd3ac8d5c9a8e8eb39be59179a370e73f63e9))
* **sso:** sign in through the provider you run yourself ([#57](https://github.com/dreulavelle/mcpd/issues/57)) ([1e22295](https://github.com/dreulavelle/mcpd/commit/1e222957e8516a021d1eb9868fe87af038851a56))
* **tunnel:** embed OpenAI's Secure MCP Tunnel in the process ([61dc44d](https://github.com/dreulavelle/mcpd/commit/61dc44d216cfc13182e11c5dcfe312ffa4864c76))
* **ui:** collapsible connections, per-plugin details, and a setup guide ([45ff318](https://github.com/dreulavelle/mcpd/commit/45ff31825dab6342e42160165ff51f2b7517cb88))
* wire the approval engine end to end ([79666d9](https://github.com/dreulavelle/mcpd/commit/79666d94b05827ee7e9e2d6da64a4f4978cc9d9e))


### Fixes

* a connector that cannot approve cannot apply anything ([14d71e2](https://github.com/dreulavelle/mcpd/commit/14d71e273e17cd3c9f555874c222ddda7aed0be9))
* a first plugin can be configured, and tunnels tell the truth about themselves ([#28](https://github.com/dreulavelle/mcpd/issues/28)) ([e1e3abd](https://github.com/dreulavelle/mcpd/commit/e1e3abd4689eae7d85992c73216fdce0505b7d4e))
* a page that throws no longer blanks the whole dashboard ([#29](https://github.com/dreulavelle/mcpd/issues/29)) ([3b4fafa](https://github.com/dreulavelle/mcpd/commit/3b4fafa7293b5aa9f280dc3efd4efcef72fdeb9b))
* a settled intent can be proposed again ([#1](https://github.com/dreulavelle/mcpd/issues/1)) ([6a628fb](https://github.com/dreulavelle/mcpd/commit/6a628fb9a86f7f662d344d6e777f6512f072606a))
* a tunnel's identity must not be written into a shared server ([8759037](https://github.com/dreulavelle/mcpd/commit/8759037fe1684698500e6b7c359b5af0feceb25e))
* **admin:** the dashboard's cookie follows the dashboard's scheme ([#45](https://github.com/dreulavelle/mcpd/issues/45)) ([9bff255](https://github.com/dreulavelle/mcpd/commit/9bff25560e57bae6977abe4223bd1568e1d55343))
* advertise plugin scopes, and ask for the ones each endpoint needs ([0208501](https://github.com/dreulavelle/mcpd/commit/0208501ad185f89a8f5eb899b527fd74dd7915ab))
* advertise scopes_supported in protected-resource metadata ([361cad6](https://github.com/dreulavelle/mcpd/commit/361cad6d46b8ef49f40863f8f32db5930c25aa73))
* **approvals:** the client's prompt is the only one a routine change needs ([#66](https://github.com/dreulavelle/mcpd/issues/66)) ([57084d8](https://github.com/dreulavelle/mcpd/commit/57084d81cf4fa0f49d153a77fbe00ff57e3758c7))
* **catalog:** drop the remote icon the CSP has always blocked ([#48](https://github.com/dreulavelle/mcpd/issues/48)) ([cc0389e](https://github.com/dreulavelle/mcpd/commit/cc0389e9e1901b3355d637053096fe9fd318908c))
* **config:** permit plaintext public_url on private networks ([85bc5d2](https://github.com/dreulavelle/mcpd/commit/85bc5d2122c2e95581191089eb8dd6011445c239))
* don't state the tunnel twice on Connections ([a078eff](https://github.com/dreulavelle/mcpd/commit/a078effa2577862096003cbc0eb12d637202db21))
* drop the password advice from the account forms ([#7](https://github.com/dreulavelle/mcpd/issues/7)) ([0116f55](https://github.com/dreulavelle/mcpd/commit/0116f551a645b1a5117af9faaae146d07d80a4e5))
* give each MCP endpoint its own OAuth identity, and connect after serving ([51cb945](https://github.com/dreulavelle/mcpd/commit/51cb9450652a1fe8a226fb20402214a8caac8443))
* **logging:** both formats take turns at the one destination ([#56](https://github.com/dreulavelle/mcpd/issues/56)) ([89c6178](https://github.com/dreulavelle/mcpd/commit/89c6178d30a1cb22dd6bf1272f5325d81fee05c0))
* **logs:** one record, one line, and the real message on it ([#64](https://github.com/dreulavelle/mcpd/issues/64)) ([b326038](https://github.com/dreulavelle/mcpd/commit/b326038f2797eec954c543b542159b827d62c669))
* make tunnels from the plugin, and stop offering dead settings ([5ac76ad](https://github.com/dreulavelle/mcpd/commit/5ac76ad9cf6103c9ab8e01385aa71bfa36bedd75))
* **mcp:** do not advertise an authorization server that is not mounted ([c5877ce](https://github.com/dreulavelle/mcpd/commit/c5877ce5fad780bf55ab129b67bc56cf41e55ec0))
* **nav:** the keys page says which keys it means ([#55](https://github.com/dreulavelle/mcpd/issues/55)) ([9049302](https://github.com/dreulavelle/mcpd/commit/9049302744dbcde86fb91354946a6a5b956fd95e))
* **observium:** a redirect means the wrong edition, and says so ([#73](https://github.com/dreulavelle/mcpd/issues/73)) ([63c83bc](https://github.com/dreulavelle/mcpd/commit/63c83bc5aa6f18ce2ee7f6e68e61e774f6efa2c0))
* **observium:** filters that matched nothing, against a real CE 26.1 ([#72](https://github.com/dreulavelle/mcpd/issues/72)) ([dcbfbe0](https://github.com/dreulavelle/mcpd/commit/dcbfbe0a70fe1b15e41038e1d8cc71a935a93803))
* **plugins:** use a plugin's published schema; recover registration panics ([589ac87](https://github.com/dreulavelle/mcpd/commit/589ac877c2fe0ed3eb714d76adc2b6b3023ccc72))
* read-only is enforced where requests leave, not where paths are built ([#27](https://github.com/dreulavelle/mcpd/issues/27)) ([fe256ec](https://github.com/dreulavelle/mcpd/commit/fe256ec2a01b3dc0ace6c412ad1d4c18135ac614))
* roles read as "User" and "Admin" ([#8](https://github.com/dreulavelle/mcpd/issues/8)) ([f60926e](https://github.com/dreulavelle/mcpd/commit/f60926e2facccf19630c3c5067ad9009344014c9))
* scope tunnel requests to an organization, and cut the page down ([0fa1e3e](https://github.com/dreulavelle/mcpd/commit/0fa1e3ecef5aa9ef9194711c2d4778f0067be4e8))
* serve https, because an OAuth issuer has to be one ([5b8f4ab](https://github.com/dreulavelle/mcpd/commit/5b8f4ab3b5a73bceeada7daa099c9a8d16c720a8))
* serve OAuth to ChatGPT connectors, and fix overlapping UI ([563487c](https://github.com/dreulavelle/mcpd/commit/563487c122b0ba37062bb94bb43f303ad114e1f2))
* settings roles, and rewrite the docs ([#6](https://github.com/dreulavelle/mcpd/issues/6)) ([ac2e2e1](https://github.com/dreulavelle/mcpd/commit/ac2e2e1eb3b89fca66198346fda18176eb11c4a9))
* **settings:** a conditional field pointed at a key its own form did not have ([#70](https://github.com/dreulavelle/mcpd/issues/70)) ([7a21818](https://github.com/dreulavelle/mcpd/commit/7a21818e1ded82f1537acd6534a88aac77fa3f3a))
* **sso:** a provider that is switched on either works or says why ([#60](https://github.com/dreulavelle/mcpd/issues/60)) ([0217963](https://github.com/dreulavelle/mcpd/commit/0217963dce87bbc73ef98c0981f273f65ce3f7a2))
* **sso:** the operator's own provider can actually be used ([#59](https://github.com/dreulavelle/mcpd/issues/59)) ([8e3b772](https://github.com/dreulavelle/mcpd/commit/8e3b77212819ebd7e737ce6c3483f4812bae5e12))
* the role collapse left two invalid defaults behind ([#10](https://github.com/dreulavelle/mcpd/issues/10)) ([1f8e158](https://github.com/dreulavelle/mcpd/commit/1f8e1581fe15f9627f6c50fa773b0dccdb83564a))
* the tunnel carries the connection, not the sign-in ([4c9d682](https://github.com/dreulavelle/mcpd/commit/4c9d68287e10dc584780131f4d80b92e3dc3e051))
* the tunnel settings ask for credentials, not a tunnel id ([#9](https://github.com/dreulavelle/mcpd/issues/9)) ([8beefbb](https://github.com/dreulavelle/mcpd/commit/8beefbbb3aca40924534e5ae733ae7f025aa62c7))
* **tunnel:** make the settings form actually drive the tunnel ([bb46307](https://github.com/dreulavelle/mcpd/commit/bb46307e44a88f0a5dc777f393c03a2d6bb541bf))
* **web:** a tunnel id sits in its row rather than towering over it ([#20](https://github.com/dreulavelle/mcpd/issues/20)) ([501b160](https://github.com/dreulavelle/mcpd/commit/501b1601c02958a5f0907ef4aabf627ef6f6f4b4))


### Refactoring

* approvals belong in the conversation, so drop the Changes tab ([2715659](https://github.com/dreulavelle/mcpd/commit/2715659c65f172f153267a342a5acbf983727c51))
* **dashboard:** plainer words, fewer comments, and no half-empty grid ([#47](https://github.com/dreulavelle/mcpd/issues/47)) ([1ed514f](https://github.com/dreulavelle/mcpd/commit/1ed514f6fce2e5a9dbf06d845222db3888a4b0d3))
* **nav:** the administrative pages are siblings, not a drawer ([#58](https://github.com/dreulavelle/mcpd/issues/58)) ([6bc043a](https://github.com/dreulavelle/mcpd/commit/6bc043a832556ce0d9dd88a74b0b72881636efc8))
* **observium:** subscription only, and one code path again ([#76](https://github.com/dreulavelle/mcpd/issues/76)) ([5e64e1b](https://github.com/dreulavelle/mcpd/commit/5e64e1b8de8149d822e1317ef45bd45f327469e8))
* **ui:** plain language and a proper design pass ([032a41c](https://github.com/dreulavelle/mcpd/commit/032a41c28c938533846a1882485fa00d659bbd7b))
* **web:** a page per job, and tunnels you can actually make ([aba07aa](https://github.com/dreulavelle/mcpd/commit/aba07aab2077799c488d25d1323f1bddc9da1b81))
* **web:** one frame for signing in, one shape for a page ([#14](https://github.com/dreulavelle/mcpd/issues/14)) ([3585ce6](https://github.com/dreulavelle/mcpd/commit/3585ce6d9593147e84472ebd0c50bc639be3edf5))


### Documentation

* **cnmaestro:** say what gates the API Clients page ([#19](https://github.com/dreulavelle/mcpd/issues/19)) ([908b062](https://github.com/dreulavelle/mcpd/commit/908b062c315e0d91cfaadc4be6858919edd45999))
* revise cnMaestro findings against API 6.3.0; add endpoint deny-list ([10fd6cb](https://github.com/dreulavelle/mcpd/commit/10fd6cba858b72bb9f36c54d0c610dd7f5a7909b))
* say which address ChatGPT actually uses ([e66e1c3](https://github.com/dreulavelle/mcpd/commit/e66e1c3c0a9e7782893509c9f5b4e8ce34d10b09))
* the README describes the host as it is now ([#3](https://github.com/dreulavelle/mcpd/issues/3)) ([c57c957](https://github.com/dreulavelle/mcpd/commit/c57c957802ca48f50c6aafed4493fea8b587bf4e))
