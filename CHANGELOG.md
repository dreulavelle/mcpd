# Changelog

## [0.21.0](https://github.com/dreulavelle/mcpd/compare/v0.20.0...v0.21.0) (2026-09-06)


### Features

* **web:** destinations, a schedule and a history on Backup & Restore ([7795975](https://github.com/dreulavelle/mcpd/commit/7795975417341d23514e8cc41d7fe41de5166e8a))


### Fixes

* **backup:** a refusal an operator reads is a sentence ([3621af8](https://github.com/dreulavelle/mcpd/commit/3621af8822c378e52c0cc129af11632b51b7a3ca))
* **web:** a credential kept across an auth switch, and a gate on the wrong permission ([127b88d](https://github.com/dreulavelle/mcpd/commit/127b88dfe50fa6caf20881a98e7ddae0e4d56147))
* **web:** a credential that is not there, and a hook behind a short circuit ([2a6a935](https://github.com/dreulavelle/mcpd/commit/2a6a93519052dfea8af535f3cbcac443c8816f16))
* **web:** stop the backup page claiming a host key it cannot vouch for ([c72330c](https://github.com/dreulavelle/mcpd/commit/c72330caa1f94e588ff513489dc940e98a0956e8))
* **web:** three ways the backup page said something untrue ([e03eebb](https://github.com/dreulavelle/mcpd/commit/e03eebb31d68185fa4d3d5c7a66506b501aef186))


### Refactoring

* **web:** let the catalog say when a backup day is worth asking for ([6c8cf36](https://github.com/dreulavelle/mcpd/commit/6c8cf360ebc4a306d88dfde1aeddc3284a2f3927))
* **web:** one Technical details block, and drop two helpers nobody calls ([97aef25](https://github.com/dreulavelle/mcpd/commit/97aef25ecf33f81fde8c1c8682f7af94b6eecff1))


### Documentation

* **backup:** name the control that forgets a pinned host key ([defdf32](https://github.com/dreulavelle/mcpd/commit/defdf32b9dcdb2883677248aeed69a54a57c4be8))

## [0.20.0](https://github.com/dreulavelle/mcpd/compare/v0.19.1...v0.20.0) (2026-09-06)


### Features

* **backup:** send archives to destinations, on a schedule ([17295ff](https://github.com/dreulavelle/mcpd/commit/17295ffae1c638890ca104908d132e1c4878fc51))
* **backup:** store destinations and the record of every run ([948dc0a](https://github.com/dreulavelle/mcpd/commit/948dc0a7306f624ca111835c807545d14f803373))
* **backup:** the destination API, and the worker that uses it ([1603a2a](https://github.com/dreulavelle/mcpd/commit/1603a2a4d931c5c339cb8794845c8fb15af2f4a2))
* **settings:** the backup schedule and the passphrase it seals with ([f8ab1b5](https://github.com/dreulavelle/mcpd/commit/f8ab1b5bc01facb9e9acbae2c7cb881f271348b3))


### Fixes

* **backup:** do not sweep a run this process has just been asked for ([da45dbb](https://github.com/dreulavelle/mcpd/commit/da45dbb9de88d7188f4be3d71047306271f862a7))
* **backup:** the first review round ([434c2a8](https://github.com/dreulavelle/mcpd/commit/434c2a8ac0c0dfb4ef370bc74eeae219f13433ef))


### Documentation

* **backup:** destinations, the schedule, and what is kept ([d8faf77](https://github.com/dreulavelle/mcpd/commit/d8faf77914609760c158a0df71b0c9823a0b20c4))

## [0.19.1](https://github.com/dreulavelle/mcpd/compare/v0.19.0...v0.19.1) (2026-09-05)


### Fixes

* **auth:** an existing account is offered the link before the registration policy is asked ([73afd76](https://github.com/dreulavelle/mcpd/commit/73afd76e75c210d1edd1d462ada3931e255c8f52))

## [0.19.0](https://github.com/dreulavelle/mcpd/compare/v0.18.0...v0.19.0) (2026-09-05)


### Features

* **auth:** connect a provider at sign-in, and invite a person to sign in with one ([#133](https://github.com/dreulavelle/mcpd/issues/133)) ([9d7d39e](https://github.com/dreulavelle/mcpd/commit/9d7d39ebd73547adb0b5d830035a00177622fdc0))

## [0.18.0](https://github.com/dreulavelle/mcpd/compare/v0.17.1...v0.18.0) (2026-09-05)


### Features

* **web:** a masthead on the overview ([bbac8c6](https://github.com/dreulavelle/mcpd/commit/bbac8c6c4c0e6fdfaa0dda2233cae03f572ae8b3))
* **web:** a sign-in panel that is the brand's own night ([512d69c](https://github.com/dreulavelle/mcpd/commit/512d69cc9e5748f09cdc2767acf81ad3cbdcc20f))
* **web:** Nord palette, and the brand mark inside the console ([495dd76](https://github.com/dreulavelle/mcpd/commit/495dd76d02e3f16fbd874cc6296bebf4bc65a52a))

## [0.17.1](https://github.com/dreulavelle/mcpd/compare/v0.17.0...v0.17.1) (2026-09-05)


### Documentation

* polish README and introduce matching mcpd branding ([#129](https://github.com/dreulavelle/mcpd/issues/129)) ([d949cbb](https://github.com/dreulavelle/mcpd/commit/d949cbb33308d02eab2d9f93a08bebcddd50bba3))

## [0.17.0](https://github.com/dreulavelle/mcpd/compare/v0.16.0...v0.17.0) (2026-09-05)


### Features

* **approvals:** a queue you can read, and the record behind it ([#124](https://github.com/dreulavelle/mcpd/issues/124)) ([dcf721e](https://github.com/dreulavelle/mcpd/commit/dcf721e356577dfa9f80bbb323e2d5e491c4f072))
* **audit:** a timeline of who did what ([#125](https://github.com/dreulavelle/mcpd/issues/125)) ([b02029e](https://github.com/dreulavelle/mcpd/commit/b02029e3d7f37e9dfebb34b0a35d0bd974192c9a))
* **dashboard:** say what happened in plain words ([#120](https://github.com/dreulavelle/mcpd/issues/120)) ([0107653](https://github.com/dreulavelle/mcpd/commit/01076533a6713c0b2871c13fada89b7d0ec6a6be))
* **overview:** a first screen that says what is happening ([#123](https://github.com/dreulavelle/mcpd/issues/123)) ([b9a9f8c](https://github.com/dreulavelle/mcpd/commit/b9a9f8c69d4aabcf1b6ecd1ed7b7b8a717a68850))


### Fixes

* **plugins:** removing a plugin forgets its settings ([#126](https://github.com/dreulavelle/mcpd/issues/126)) ([d8b73ef](https://github.com/dreulavelle/mcpd/commit/d8b73ef155205db7b267887b08502e5e80f5bc68))
* **web:** a plugin is enabled or disabled with a switch, and removed in one line ([#127](https://github.com/dreulavelle/mcpd/issues/127)) ([ecd7ab7](https://github.com/dreulavelle/mcpd/commit/ecd7ab73be3f265467702018341bd55587c8b93c))


### Refactoring

* **dashboard:** plain words on every page ([#122](https://github.com/dreulavelle/mcpd/issues/122)) ([a0bae08](https://github.com/dreulavelle/mcpd/commit/a0bae08480e377d3681708459574adc1e2e58585))

## [0.16.0](https://github.com/dreulavelle/mcpd/compare/v0.15.0...v0.16.0) (2026-09-05)


### Features

* **bookstack:** read the knowledge base, and write to it under approval ([e1963b6](https://github.com/dreulavelle/mcpd/commit/e1963b68f63f7d4f92a522aa5bf4448aaf01fb09))


### Fixes

* **threecx:** two customers spelt the same are refused ([0fcc66b](https://github.com/dreulavelle/mcpd/commit/0fcc66b21ecd0de7596e169cb8a0ada84358b38d))

## [0.15.0](https://github.com/dreulavelle/mcpd/compare/v0.14.1...v0.15.0) (2026-09-05)


### Features

* **flowroute:** read your customers' carrier accounts, one key each ([7d8dd99](https://github.com/dreulavelle/mcpd/commit/7d8dd993f18cb89776ab5c67323f47464fecc404))


### Documentation

* write down the customer roster design, and table it ([f2a2376](https://github.com/dreulavelle/mcpd/commit/f2a23766a6a39cfa6a10000cef650d156ce7df7a))

## [0.14.1](https://github.com/dreulavelle/mcpd/compare/v0.14.0...v0.14.1) (2026-09-04)


### Fixes

* **bandwidth:** read every 10DLC campaign, not the second page onward ([876df2d](https://github.com/dreulavelle/mcpd/commit/876df2dd19a938df66cf1c2767a742d8b9450f45))

## [0.14.0](https://github.com/dreulavelle/mcpd/compare/v0.13.0...v0.14.0) (2026-09-04)


### Features

* **plugins:** a type can say how to use it ([32efcd9](https://github.com/dreulavelle/mcpd/commit/32efcd9f50f5c9acd145ac70b4f01daf08151f04))
* **sdk:** an out-of-process plugin is a type, and its settings reach it ([e785fa9](https://github.com/dreulavelle/mcpd/commit/e785fa919ea763e36404145a8c327bac265527e5))
* **settings:** a plugin setting can be a table of rows ([71c8aee](https://github.com/dreulavelle/mcpd/commit/71c8aeea31c14fb5f550067dcf52ddde233f4659))
* **threecx:** one instance serves many customers ([2c42250](https://github.com/dreulavelle/mcpd/commit/2c42250adf3a35a0c7781ed142c96163fa310d5c))
* **threecx:** probe customers together, and prove the plugin through the host ([89634df](https://github.com/dreulavelle/mcpd/commit/89634df4e2f304701882bc26cfed497b42dfde92))
* **threecx:** read a 3CX v20 phone system ([402de9a](https://github.com/dreulavelle/mcpd/commit/402de9ae763084128fe886b2c36e5af4592e9bd5))
* **threecx:** read the support bundle as a digest, as a last resort ([5486dc3](https://github.com/dreulavelle/mcpd/commit/5486dc3978e2f13e1c2d6dc99ff393ab4e6f6b64))
* **threecx:** read what the phone system is refusing, and remote connectivity ([2ab0327](https://github.com/dreulavelle/mcpd/commit/2ab0327f080b3fa5086b7e4ca887b267149bac1a))


### Fixes

* **settings:** an https address reads without its scheme ([2cef303](https://github.com/dreulavelle/mcpd/commit/2cef303e23c69feaa06cd907ab0522ec89f94166))
* **threecx:** a port is part of the address, and a trailing slash is not ([7a92da2](https://github.com/dreulavelle/mcpd/commit/7a92da2276c963619e10827e48ebe3cb5ad1a0bf))
* **threecx:** an unknown customer says where to add it ([4ab34ea](https://github.com/dreulavelle/mcpd/commit/4ab34eae77bdfbd3bddeca426b5ca1e83723fa3b))
* **threecx:** reach a customer's phone system only when asked ([4de4666](https://github.com/dreulavelle/mcpd/commit/4de4666b80db1da4af90aeed3d519928b35a7c5f))
* **threecx:** read a capture's state under the lock that guards it ([53a8054](https://github.com/dreulavelle/mcpd/commit/53a805485b55f4f2e8d7f82e4365f6ce15547190))
* **threecx:** say what it does in a sentence ([b100fbd](https://github.com/dreulavelle/mcpd/commit/b100fbd1321ad4ba72ee89a7773e88695998a3a3))

## [0.13.0](https://github.com/dreulavelle/mcpd/compare/v0.12.0...v0.13.0) (2026-09-03)


### Features

* **auth:** editable roles, per-plugin grants, and additive groups ([40bd2fd](https://github.com/dreulavelle/mcpd/commit/40bd2fd164443e4d12141ac3f52b4665e9ca3041))
* **tunnel:** an account owns its workspaces, and a tunnel may belong to several accounts ([f9547db](https://github.com/dreulavelle/mcpd/commit/f9547db9420df8d3ee73878463eecaa17f841579))
* **tunnel:** making a tunnel is one call, and the host supplies the workspaces ([bd66a71](https://github.com/dreulavelle/mcpd/commit/bd66a7134eaf0549f6cd64b39ac5b740088c1d1f))
* **tunnel:** one authority per tunnel, and the account that owns it is a fact ([3518863](https://github.com/dreulavelle/mcpd/commit/351886366dbb508baa44e4240c5d692b515793aa))
* **web:** a Clients page for reaching this host from Claude Code, Codex and an IDE ([6729a3d](https://github.com/dreulavelle/mcpd/commit/6729a3d738b68255fa172726de178bcdf8a8af5b))
* **web:** a settings rail, and access pages that read as a set ([61ab74c](https://github.com/dreulavelle/mcpd/commit/61ab74c12d3f822841282d6f4b2e8b113327a22c))
* **web:** an unset address falls back to the host this page is on ([99cb6dc](https://github.com/dreulavelle/mcpd/commit/99cb6dc1aa39d41f201c2879ea00ab5c97323031))
* **web:** the Clients page is one panel ([7b2f7a0](https://github.com/dreulavelle/mcpd/commit/7b2f7a06839e5a91d30881171dcbf20f67c8015e))


### Fixes

* **tunnel:** a tunnel assigned to the wrong account is named as such, and moved with one press ([07ae8a5](https://github.com/dreulavelle/mcpd/commit/07ae8a5e5ab394662c9c7c86e38a9cc1b608c3fb))
* **tunnel:** a tunnel OpenAI has already deleted can be removed here ([b7d69ad](https://github.com/dreulavelle/mcpd/commit/b7d69adb2cdba6ff72060ec56b7f45865f40352d))
* **tunnel:** say which of two things a refused create means ([d2a68d7](https://github.com/dreulavelle/mcpd/commit/d2a68d7e20828d7787a54daebb7d85bf3940418d))
* **web:** a client snippet never prints a bare path ([72dd3bc](https://github.com/dreulavelle/mcpd/commit/72dd3bcf69cbda893e09471f4e9ea1af424d94e5))
* **web:** a plugin's diagnosis stays on its page; a table cell gets the first lines ([06fc66d](https://github.com/dreulavelle/mcpd/commit/06fc66d040fea4f166ef01802998f4fc6ed8efee))
* **web:** a table shows the upstream's status number; the plugin's page shows the diagnosis ([5e48077](https://github.com/dreulavelle/mcpd/commit/5e480772dba6673cbf0f0ca1c20ea1e2c7ecf994))
* **web:** settings pages are centred like every other, in a wider column ([004643e](https://github.com/dreulavelle/mcpd/commit/004643ed8263056bd11f9ecc636163f277e51ccc))
* **web:** settings pages take the full width, with the rail at the left edge ([c490cae](https://github.com/dreulavelle/mcpd/commit/c490caeedaf48e7e55a2b81be47b0b4f9416c9bd))
* **web:** several tunnels for one plugin is not something to flag ([72b24b6](https://github.com/dreulavelle/mcpd/commit/72b24b6a91d6382e8ff8b1aff4a344cf6cb21e17))

## [0.12.0](https://github.com/dreulavelle/mcpd/compare/v0.11.0...v0.12.0) (2026-09-02)


### Features

* **tunnel:** supervise a tunnel, and stop taking "connected" at its word ([5d1ba98](https://github.com/dreulavelle/mcpd/commit/5d1ba98292d81819d156c5cd1557256a33f395eb))
* **web:** a tunnel's detail as a sheet, metrics behind the bars, and the calls that explain them ([be76d46](https://github.com/dreulavelle/mcpd/commit/be76d46a8dce9cfa45c42674cf08a75f28ee4c20))
* **web:** the tunnels page as a list and an inspector ([da595f0](https://github.com/dreulavelle/mcpd/commit/da595f01cbc7e6e7e6f85179b4f29c9ecb14e210))
* **web:** tunnels by account, what each is doing, and the step ChatGPT needs ([684f165](https://github.com/dreulavelle/mcpd/commit/684f165c87aa3a9fadd9b49302124b22a72df044))


### Fixes

* **tunnel:** report a rejected key once ([71af191](https://github.com/dreulavelle/mcpd/commit/71af1917a4df6c98c2d91b707c773e95cec55a39))
* **tunnel:** say what a refused key means, in one breath, and stop calling an idle connector unattached ([b77fff2](https://github.com/dreulavelle/mcpd/commit/b77fff2662e9d4bce7bcd6d181e2bb5d971d74ed))

## [0.11.0](https://github.com/dreulavelle/mcpd/compare/v0.10.0...v0.11.0) (2026-09-02)


### Features

* **web:** a command palette and keyboard shortcuts ([9a91d05](https://github.com/dreulavelle/mcpd/commit/9a91d05bce4c4c9c8569e487852b972fc45862fe))
* **web:** choose an appearance, or keep following the system ([912b288](https://github.com/dreulavelle/mcpd/commit/912b2883cf46cc2dddcb824fbeeac3549dccfa21))
* **web:** find a tool, follow a plugin to its calls, and keep a log line ([9a18d0f](https://github.com/dreulavelle/mcpd/commit/9a18d0f9ff7eeea5dec84dcb97a72557aded7ab3))
* **web:** the console's own confirmation, a warning before the session ends, and audit filters ([aa0d0d8](https://github.com/dreulavelle/mcpd/commit/aa0d0d85bf52708b6606591aced5701f367d0b3b))
* **web:** titles that say where you are, a way past the sidebar, and a plugin finder ([c28d45f](https://github.com/dreulavelle/mcpd/commit/c28d45fd0329fe170a94005fe7c68a80db34a741))
* **web:** what each account may actually do, and keys that can be re-scoped ([48ba9e5](https://github.com/dreulavelle/mcpd/commit/48ba9e5973380cba58e718a1c29a7229baf675c7))
* **web:** what needs attention, first, and filters that live in the address ([6063af6](https://github.com/dreulavelle/mcpd/commit/6063af6dbd08bbc6eb927f7cd1cece9d9fe2af4d))

## [0.10.0](https://github.com/dreulavelle/mcpd/compare/v0.9.1...v0.10.0) (2026-09-02)


### Features

* **auth:** a group may take capabilities away ([9d16b43](https://github.com/dreulavelle/mcpd/commit/9d16b43ac1b22e202441c973854deb50d782a2ef))
* **bandwidth:** disconnect detail, and port-out passcodes behind a capability ([8574849](https://github.com/dreulavelle/mcpd/commit/85748491e67f39dd8e81797e38051cc3f5249910))
* **bandwidth:** per-number reads, line records, and Insights call events ([65cfe09](https://github.com/dreulavelle/mcpd/commit/65cfe0923d274bd2ac74d05ef62e7af8337bbbb8))
* **bandwidth:** read port-outs and account entitlements ([3b1d908](https://github.com/dreulavelle/mcpd/commit/3b1d90839b6d3944c937b9dc8fa5e151dfddbfe0))
* **textable:** read a Textable instance as a service account ([20205c8](https://github.com/dreulavelle/mcpd/commit/20205c89cb31d0e4ac8628728a3bde0be14f156b))


### Fixes

* **auth:** a group ceiling may not strand the host without an administrator ([00e3b13](https://github.com/dreulavelle/mcpd/commit/00e3b13ad94b96f3e6b19afc7a5830698909a93b))
* **auth:** a group must not widen a subject's own grant ([c1d49df](https://github.com/dreulavelle/mcpd/commit/c1d49df7793fecb6b6f6ec710ae0e0cf6afff51f))
* **auth:** the console draws its controls from what a session may actually do ([16a2592](https://github.com/dreulavelle/mcpd/commit/16a25924f4e79d8a9ec9f7abb871a56c61efe3b4))
* **bandwidth:** read 10DLC from the API that serves it ([6f173bb](https://github.com/dreulavelle/mcpd/commit/6f173bb868e2cd9d79faa66d32865cfcdca77d3b))
* **bandwidth:** read a disconnect order's notes from the path the allow-list knows ([b42b6ce](https://github.com/dreulavelle/mcpd/commit/b42b6ce2e878d3c1f94c26347292e13e20f997d3))
* **textable:** a degraded instance is an answer, not a failed start ([8f6d0d6](https://github.com/dreulavelle/mcpd/commit/8f6d0d60c2b89aa97e29b3d98dc004cc475b8d93))
* **textable:** describe the token this integration actually takes ([20b8226](https://github.com/dreulavelle/mcpd/commit/20b82269e7c3fa37dc88044442eee78322a6b454))
* **web:** an expired session returns to the sign-in form ([4eba7cf](https://github.com/dreulavelle/mcpd/commit/4eba7cf89c1f1f59b27a7cfe23f1a145012d1a62))
* **web:** editing a ChatGPT account keeps its stored admin key ([ed0ca3b](https://github.com/dreulavelle/mcpd/commit/ed0ca3b37936805bde12d4d4a182fc0bcabc6b1d))
* **web:** smaller dashboard corrections ([6676aea](https://github.com/dreulavelle/mcpd/commit/6676aea22b362bab5c0807e3b84a5f60ba7b012f))
* **web:** the activity list keeps the pages it was asked for ([dbb36ce](https://github.com/dreulavelle/mcpd/commit/dbb36ce878fcd0f58f6dd68adb6a9ca85c11d3fd))

## [0.9.1](https://github.com/dreulavelle/mcpd/compare/v0.9.0...v0.9.1) (2026-09-01)


### Fixes

* **tunnel:** let two ChatGPT accounts share one plugin ([f10388c](https://github.com/dreulavelle/mcpd/commit/f10388cfa82708e12083b819ee1c640cdd5ede6c))
* **tunnel:** offer only the selected account's workspaces, and stop refetching them ([aaf138a](https://github.com/dreulavelle/mcpd/commit/aaf138adb1eccc5006361c6ae288d3664c527de8))
* **tunnel:** show an OpenAI refusal as a page, and say what it actually means ([#108](https://github.com/dreulavelle/mcpd/issues/108)) ([5d47e38](https://github.com/dreulavelle/mcpd/commit/5d47e38c5ab5c55142c2c6c0644f4c7442db6d76))

## [0.9.0](https://github.com/dreulavelle/mcpd/compare/v0.8.0...v0.9.0) (2026-08-31)


### Features

* **bandwidth:** read a Bandwidth estate — numbers, porting, messaging and 10DLC ([#106](https://github.com/dreulavelle/mcpd/issues/106)) ([cf24d91](https://github.com/dreulavelle/mcpd/commit/cf24d91bce51c01d34d079539bb5c2277b945762))

## [0.8.0](https://github.com/dreulavelle/mcpd/compare/v0.7.1...v0.8.0) (2026-08-31)


### Features

* **notify:** a Discord shape that says how much attention it wants ([c640947](https://github.com/dreulavelle/mcpd/commit/c64094788c1b1e028f4c04520b19530f29af1586))
* **observability:** keep the log in a file that outlives the container ([225b296](https://github.com/dreulavelle/mcpd/commit/225b296428641c6aa305c2658e83de5e09fdf382))
* **tunnels:** tell somebody when a connector stops ([84a03e8](https://github.com/dreulavelle/mcpd/commit/84a03e84e918cb4f80f018b66abcb4e2567778b5))


### Fixes

* **extremecloudiq:** stop reporting a sliding session window as the key's expiry ([33241b0](https://github.com/dreulavelle/mcpd/commit/33241b0b1855c5bcf2da32f9ee9178e94a849f78))

## [0.7.1](https://github.com/dreulavelle/mcpd/compare/v0.7.0...v0.7.1) (2026-08-31)


### Documentation

* say why a merge commit must not repeat the branch's subject ([274a4fd](https://github.com/dreulavelle/mcpd/commit/274a4fd5d652e59ee3596a4da03ed3f516ab3530))

## [0.7.0](https://github.com/dreulavelle/mcpd/compare/v0.6.1...v0.7.0) (2026-08-30)


### Features

* reach MCP servers whose documents declare no credential, and import client configs ([#99](https://github.com/dreulavelle/mcpd/issues/99)) ([031c653](https://github.com/dreulavelle/mcpd/commit/031c6538f0587144901b6479ec9fdee39fe5156d))
* **backup:** take a whole instance as one encrypted file, and put it back ([4f4e91c](https://github.com/dreulavelle/mcpd/commit/4f4e91c86bda0c9f8e0a9cf2c49078e3b61f460c))
* **mcpservers:** re-ask each remote server what it offers, on a timer ([d1d1b6d](https://github.com/dreulavelle/mcpd/commit/d1d1b6d3e920a278bb8fb5404e489dcdd0c1a9eb))
* **observability:** record who called what, and show it ([7ff8b03](https://github.com/dreulavelle/mcpd/commit/7ff8b03de63d47a091493964847b04cbaaf9d57d))
* **approvals:** a window that stops the asking, and closes on its own ([68b11b1](https://github.com/dreulavelle/mcpd/commit/68b11b1eceae1bf80b53d348007a2685974e9a36))
* **notify:** tell an operator what happened, and never ask them to approve it ([61976c4](https://github.com/dreulavelle/mcpd/commit/61976c479c827dc8327380598efd0a6b09dd0fd3))
* **registry:** a catalogue of your own, fetched from wherever you keep it ([14a6aeb](https://github.com/dreulavelle/mcpd/commit/14a6aeb59528450f3f6ad6039deb0d3d2cd53946))


### Fixes

* **dashboard:** let the Settings tab be the only way into users and groups ([9d1fc79](https://github.com/dreulavelle/mcpd/commit/9d1fc79ce3c11826e4919d8d1279cb4c09c662a6))
* three bugs a review of the last four features turned up, and their docs ([482a64d](https://github.com/dreulavelle/mcpd/commit/482a64df67f6eb19ad7d89881d439c9b871f5f27))

## [0.6.1](https://github.com/dreulavelle/mcpd/compare/v0.6.0...v0.6.1) (2026-08-27)


### Fixes

* **deploy:** make ./data before the first start, or the container cannot write ([#97](https://github.com/dreulavelle/mcpd/issues/97)) ([e5e0b8b](https://github.com/dreulavelle/mcpd/commit/e5e0b8b34e7902c2aa5ea903a6e30aec75a76cf8))

## [0.6.0](https://github.com/dreulavelle/mcpd/compare/v0.5.0...v0.6.0) (2026-08-27)


### Features

* **deploy:** run a published release without building it first ([#94](https://github.com/dreulavelle/mcpd/issues/94)) ([47ad408](https://github.com/dreulavelle/mcpd/commit/47ad4082181ffdd7596bc7b780893efeafc72a4d))
* **extremecloudiq:** read an Extreme Networks estate, and say what is wrong with it ([#94](https://github.com/dreulavelle/mcpd/issues/94)) ([47ad408](https://github.com/dreulavelle/mcpd/commit/47ad4082181ffdd7596bc7b780893efeafc72a4d))


### Fixes

* **tunnel:** serialise a tunnel's lifecycle so two settings changes cannot race ([#95](https://github.com/dreulavelle/mcpd/issues/95)) ([27b851e](https://github.com/dreulavelle/mcpd/commit/27b851ed2b860753abbfaa87a10bee9c0bba986d))


### Documentation

* **readme:** lead with what mcpd does for somebody, not how it is built ([#94](https://github.com/dreulavelle/mcpd/issues/94)) ([47ad408](https://github.com/dreulavelle/mcpd/commit/47ad4082181ffdd7596bc7b780893efeafc72a4d))

## [0.5.0](https://github.com/dreulavelle/mcpd/compare/v0.4.1...v0.5.0) (2026-08-27)


### Features

* **chatgpt:** connect several ChatGPT accounts, each with its own identity ([#91](https://github.com/dreulavelle/mcpd/issues/91)) ([ef97f7f](https://github.com/dreulavelle/mcpd/commit/ef97f7f25b6ba6286c9b6d14205417512219b1fc))
* **marketplace:** hold the catalogues rather than proxying them ([#92](https://github.com/dreulavelle/mcpd/issues/92)) ([0521ede](https://github.com/dreulavelle/mcpd/commit/0521ede7f372ac619c5da0b3a33dc0e64f09b442))
* **settings:** one navigation, and groups that say where they live ([#93](https://github.com/dreulavelle/mcpd/issues/93)) ([8be1063](https://github.com/dreulavelle/mcpd/commit/8be106385cf3150c2bebfb093fb04aa098b8e1ff))


### Fixes

* **dashboard:** let a dialog taller than the window be scrolled ([#90](https://github.com/dreulavelle/mcpd/issues/90)) ([b7d8737](https://github.com/dreulavelle/mcpd/commit/b7d8737f3c6cd9712982df6b4430e2a7dd8ecec4))
* **performance:** report what was measured, not where a boundary fell ([#88](https://github.com/dreulavelle/mcpd/issues/88)) ([169f9fa](https://github.com/dreulavelle/mcpd/commit/169f9fa049af2473b9731a4c31f66f064e93abd9))

## [0.4.1](https://github.com/dreulavelle/mcpd/compare/v0.4.0...v0.4.1) (2026-08-27)


### Fixes

* **dashboard:** render release notes, and move the version off the account ([#86](https://github.com/dreulavelle/mcpd/issues/86)) ([3fb1a35](https://github.com/dreulavelle/mcpd/commit/3fb1a35193102cb1a230ec53444d850be97016af))

## [0.4.0](https://github.com/dreulavelle/mcpd/compare/v0.3.0...v0.4.0) (2026-08-27)


### Features

* measure what a call costs, and stop paying twice ([#84](https://github.com/dreulavelle/mcpd/issues/84)) ([6b77c2e](https://github.com/dreulavelle/mcpd/commit/6b77c2e429cfe48f00b1ed2c3d9ebc210fd4e54d))

## [0.3.0](https://github.com/dreulavelle/mcpd/compare/v0.2.0...v0.3.0) (2026-08-27)


### Features

* **graylog:** read a log platform, and hold every plugin to one standard ([#80](https://github.com/dreulavelle/mcpd/issues/80)) ([395892f](https://github.com/dreulavelle/mcpd/commit/395892f6417bcbd7f77044cc861943a589f52e68))
* **trust:** trust your own certificate authority, and say which instance is which ([#82](https://github.com/dreulavelle/mcpd/issues/82)) ([f2143d8](https://github.com/dreulavelle/mcpd/commit/f2143d8ffb74c56f5fa8453f3552c313ba053a42))


### Fixes

* **version:** the binary carries its own version, and there is no "dev" ([#83](https://github.com/dreulavelle/mcpd/issues/83)) ([12116a4](https://github.com/dreulavelle/mcpd/commit/12116a43c848f5e7c6e03630f7a48c494e82f6d1))

## [0.2.0](https://github.com/dreulavelle/mcpd/compare/v0.1.0...v0.2.0) (2026-08-26)


### Features

* .env support, mcpd -init, and OpenAI Secure MCP Tunnel in compose ([a28cd01](https://github.com/dreulavelle/mcpd/commit/a28cd01489f5d81986baaff45242a89936054cbe))
* a debug switch for the tunnel, and compare Debug in SameAs ([d584c82](https://github.com/dreulavelle/mcpd/commit/d584c8265377c98409a095d4ac5353bc8014a9c1))
* a new instance asks for its first account, and there are two roles ([#5](https://github.com/dreulavelle/mcpd/issues/5)) ([12013e6](https://github.com/dreulavelle/mcpd/commit/12013e68b5404198930a9000c0a758901ab746e5))
* a plugin's settings are declared by the plugin and edited in the dashboard ([#23](https://github.com/dreulavelle/mcpd/issues/23)) ([9787bc0](https://github.com/dreulavelle/mcpd/commit/9787bc056b8d1faec0d08b5a60c3a73d71cf201d))
* add and remove plugin instances from the dashboard ([#25](https://github.com/dreulavelle/mcpd/issues/25)) ([8f10873](https://github.com/dreulavelle/mcpd/commit/8f10873f53eb94dd6dd6e102beaa1d9f791f4875))
* **admin:** operator dashboard on its own listener, port 80 by default ([3f8819c](https://github.com/dreulavelle/mcpd/commit/3f8819c718fc24f8875585209c7de0ea27aa7b4b))
* aggregate /mcp endpoint, and run under Docker ([26929eb](https://github.com/dreulavelle/mcpd/commit/26929eb112387383ffc5b4268ed72bd586a4afc2))
* **approval:** ask in the conversation instead of the dashboard ([19ea974](https://github.com/dreulavelle/mcpd/commit/19ea974a4da7305124abe7956530141c6c7f7058))
* **approvals:** a change nobody wrote a rule about goes ahead ([#65](https://github.com/dreulavelle/mcpd/issues/65)) ([f23a99c](https://github.com/dreulavelle/mcpd/commit/f23a99c0eb2ceaabf7c92b27be88389d502d6a1a))
* **auth:** built-in OAuth 2.1 authorization server ([9b9bbf8](https://github.com/dreulavelle/mcpd/commit/9b9bbf8b508cf77ec5912f62820006b4acc475c5))
* **cnmaestro:** authenticate, and read networks and devices ([#17](https://github.com/dreulavelle/mcpd/issues/17)) ([8051408](https://github.com/dreulavelle/mcpd/commit/8051408bc2c5962328337eec0f475ba4ee8bb463))
* **cnmaestro:** list managed accounts, and get managed_account right ([#18](https://github.com/dreulavelle/mcpd/issues/18)) ([2bc0af2](https://github.com/dreulavelle/mcpd/commit/2bc0af2edb07fb9c4c296f67f212e57e5d2a67ac))
* **cnmaestro:** reference plugin with read tools and typed mutations ([c2b2b0e](https://github.com/dreulavelle/mcpd/commit/c2b2b0ebe5fbde7c1edcde7f4ae63e9ea472e76c))
* **cnmaestro:** thirteen more read tools, covering what the API actually offers ([#30](https://github.com/dreulavelle/mcpd/issues/30)) ([0795d7d](https://github.com/dreulavelle/mcpd/commit/0795d7d3f3fb89a149c38fed7b396b7325d66a29))
* **config:** five lines in a file, and everything else where it is recorded ([#54](https://github.com/dreulavelle/mcpd/issues/54)) ([2117868](https://github.com/dreulavelle/mcpd/commit/21178681902403ccb785d64b0f2fe7308bab66d1))
* consistent database backups with mcpd -backup ([037bc2d](https://github.com/dreulavelle/mcpd/commit/037bc2dd598e6cb61a92921618b2752e649d77c0))
* **dashboard:** the policy has a page, and an auto-approval says nobody was asked ([#41](https://github.com/dreulavelle/mcpd/issues/41)) ([ec3ff22](https://github.com/dreulavelle/mcpd/commit/ec3ff22b422ce3a086db7b2228dd84b95fcc3d79))
* expose the tunnel client's own diagnostics ([f344be5](https://github.com/dreulavelle/mcpd/commit/f344be59129e427cfdd7cfdada7c6b140b7a9931))
* history retention and clearing; drop the second-approver rule ([4d581d7](https://github.com/dreulavelle/mcpd/commit/4d581d7372958511cc6a7f063a291a0cd103dd70))
* link to where the last manual step happens ([#13](https://github.com/dreulavelle/mcpd/issues/13)) ([d1a2480](https://github.com/dreulavelle/mcpd/commit/d1a2480b60c70a97b2bf20fb2bf669161def60df))
* **logs:** watch what this host is doing, from the dashboard ([#61](https://github.com/dreulavelle/mcpd/issues/61)) ([c5f5680](https://github.com/dreulavelle/mcpd/commit/c5f568022be0102406e0e5797621e6e8646bf4d3))
* MCP host, per-plugin auth scoping, and Docker setup ([b73afc8](https://github.com/dreulavelle/mcpd/commit/b73afc8b8014afdd9be064ab83a863a2237e4dde))
* **observability:** crash reporting that does not carry the customer with it ([#74](https://github.com/dreulavelle/mcpd/issues/74)) ([b18c537](https://github.com/dreulavelle/mcpd/commit/b18c5375ffbcf4e17227649c154318612f7cefd7))
* **observability:** logs a support call can actually use ([#75](https://github.com/dreulavelle/mcpd/issues/75)) ([a1508b5](https://github.com/dreulavelle/mcpd/commit/a1508b56a248c20f0df7a73420828e2119835b75))
* **observium:** read an SNMP estate without pretending to have its history ([#67](https://github.com/dreulavelle/mcpd/issues/67)) ([9943ddc](https://github.com/dreulavelle/mcpd/commit/9943ddcc69a8eaaa689161b57cb17adbd27da785))
* **observium:** read Community Edition, where there is no API to read ([#69](https://github.com/dreulavelle/mcpd/issues/69)) ([e24668c](https://github.com/dreulavelle/mcpd/commit/e24668c7245549b9e8c6d9c2c7405e4b0c48ee8e))
* **observium:** use the API we are actually pointed at ([#77](https://github.com/dreulavelle/mcpd/issues/77)) ([b8c28ad](https://github.com/dreulavelle/mcpd/commit/b8c28ad41e6b3f982599601e83bceba2e43ebe56))
* one integration can be configured more than once ([#21](https://github.com/dreulavelle/mcpd/issues/21)) ([230a77c](https://github.com/dreulavelle/mcpd/commit/230a77ccd896c4cf51ac894bfc2ec1e74a6b7fcd))
* one tunnel per plugin, so a connector can serve one system ([4e6f4aa](https://github.com/dreulavelle/mcpd/commit/4e6f4aa00780f7726f5efac4f87f9759bda6ecd7))
* operations domain and SQLite storage foundation ([386dcb7](https://github.com/dreulavelle/mcpd/commit/386dcb74dcaa78f7991e7ba1e935aa026692633a))
* **operations:** approval service, executor, verifier, reaper, event bus ([b12550f](https://github.com/dreulavelle/mcpd/commit/b12550f768a337e41d3048d8576ba499657e4473))
* people sign in with an email and a password ([#2](https://github.com/dreulavelle/mcpd/issues/2)) ([f59f5f2](https://github.com/dreulavelle/mcpd/commit/f59f5f210554852667ddceb7835cbecdfd5b8377))
* pick the workspace when making a tunnel ([#12](https://github.com/dreulavelle/mcpd/issues/12)) ([0d7cd60](https://github.com/dreulavelle/mcpd/commit/0d7cd6091129da1ecf3ee0bc3d4a343fd4528fbe))
* **plugins:** resources, prompts, per-tool capability and rate limits ([#24](https://github.com/dreulavelle/mcpd/issues/24)) ([c43020a](https://github.com/dreulavelle/mcpd/commit/c43020a129b8eea1a54b1fca6076a723e966255f))
* **registry:** read every published server.json format, and browse Docker's catalogue too ([#37](https://github.com/dreulavelle/mcpd/issues/37)) ([3830ad0](https://github.com/dreulavelle/mcpd/commit/3830ad045a499c6bda4953e7978f60235b1cdf64))
* **registry:** two more catalogues, and one reader for the two that agree ([#39](https://github.com/dreulavelle/mcpd/issues/39)) ([ab7de3e](https://github.com/dreulavelle/mcpd/commit/ab7de3eb5c5fd3555bdb330733e73d799654f36f))
* scope new tunnels to a workspace, and stop asking for what we know ([#11](https://github.com/dreulavelle/mcpd/issues/11)) ([c1e5b86](https://github.com/dreulavelle/mcpd/commit/c1e5b86af02a07197a9f59c4b2b58948b7096427))
* **sdk:** out-of-process plugins and the Go SDK for writing them ([c8a5443](https://github.com/dreulavelle/mcpd/commit/c8a54436ad7313e8c75507097b312bd7f2e4c7e6))
* **settings:** a catalog, so a plugin can declare its own settings ([#22](https://github.com/dreulavelle/mcpd/issues/22)) ([004c98c](https://github.com/dreulavelle/mcpd/commit/004c98c548c9d7ddea00aec2d7073dca6db7d0d2))
* **settings:** a field can depend on the answer to another one ([#68](https://github.com/dreulavelle/mcpd/issues/68)) ([42918b2](https://github.com/dreulavelle/mcpd/commit/42918b26e92f580a42adc4fba38ead70846542f4))
* **settings:** an enum can name its values in the words an operator uses ([#71](https://github.com/dreulavelle/mcpd/issues/71)) ([794efdb](https://github.com/dreulavelle/mcpd/commit/794efdb2f255e864f4ad39e656a96a20dca5eab4))
* **settings:** manage configuration from the dashboard ([4e8dd3a](https://github.com/dreulavelle/mcpd/commit/4e8dd3ac8d5c9a8e8eb39be59179a370e73f63e9))
* **sso:** sign in through the provider you run yourself ([#57](https://github.com/dreulavelle/mcpd/issues/57)) ([e8f8ad4](https://github.com/dreulavelle/mcpd/commit/e8f8ad49ffccf72bd78cd4a92833cb10d26ac4c8))
* **tunnel:** embed OpenAI's Secure MCP Tunnel in the process ([61dc44d](https://github.com/dreulavelle/mcpd/commit/61dc44d216cfc13182e11c5dcfe312ffa4864c76))
* **ui:** collapsible connections, per-plugin details, and a setup guide ([45ff318](https://github.com/dreulavelle/mcpd/commit/45ff31825dab6342e42160165ff51f2b7517cb88))
* wire the approval engine end to end ([79666d9](https://github.com/dreulavelle/mcpd/commit/79666d94b05827ee7e9e2d6da64a4f4978cc9d9e))


### Fixes

* a connector that cannot approve cannot apply anything ([14d71e2](https://github.com/dreulavelle/mcpd/commit/14d71e273e17cd3c9f555874c222ddda7aed0be9))
* a first plugin can be configured, and tunnels tell the truth about themselves ([#28](https://github.com/dreulavelle/mcpd/issues/28)) ([b3bc27c](https://github.com/dreulavelle/mcpd/commit/b3bc27cc4ae9d213267fe0f6f93c77f02a24f221))
* a page that throws no longer blanks the whole dashboard ([#29](https://github.com/dreulavelle/mcpd/issues/29)) ([2a9ea68](https://github.com/dreulavelle/mcpd/commit/2a9ea686bf3b89d5409be464e082311ad359d924))
* a settled intent can be proposed again ([#1](https://github.com/dreulavelle/mcpd/issues/1)) ([6a628fb](https://github.com/dreulavelle/mcpd/commit/6a628fb9a86f7f662d344d6e777f6512f072606a))
* a tunnel's identity must not be written into a shared server ([8759037](https://github.com/dreulavelle/mcpd/commit/8759037fe1684698500e6b7c359b5af0feceb25e))
* **admin:** the dashboard's cookie follows the dashboard's scheme ([#45](https://github.com/dreulavelle/mcpd/issues/45)) ([4218c57](https://github.com/dreulavelle/mcpd/commit/4218c5712fa1fe2207c997bae808e1c031a028e0))
* advertise plugin scopes, and ask for the ones each endpoint needs ([0208501](https://github.com/dreulavelle/mcpd/commit/0208501ad185f89a8f5eb899b527fd74dd7915ab))
* advertise scopes_supported in protected-resource metadata ([361cad6](https://github.com/dreulavelle/mcpd/commit/361cad6d46b8ef49f40863f8f32db5930c25aa73))
* **approvals:** the client's prompt is the only one a routine change needs ([#66](https://github.com/dreulavelle/mcpd/issues/66)) ([1d51a0d](https://github.com/dreulavelle/mcpd/commit/1d51a0dea9b3ce96935708e9570f4fe6309dae65))
* **catalog:** drop the remote icon the CSP has always blocked ([#48](https://github.com/dreulavelle/mcpd/issues/48)) ([a088871](https://github.com/dreulavelle/mcpd/commit/a08887186f87563c946a792f706be5dc3bf6d5cd))
* **config:** permit plaintext public_url on private networks ([85bc5d2](https://github.com/dreulavelle/mcpd/commit/85bc5d2122c2e95581191089eb8dd6011445c239))
* don't state the tunnel twice on Connections ([a078eff](https://github.com/dreulavelle/mcpd/commit/a078effa2577862096003cbc0eb12d637202db21))
* drop the password advice from the account forms ([#7](https://github.com/dreulavelle/mcpd/issues/7)) ([0be8f57](https://github.com/dreulavelle/mcpd/commit/0be8f57e70cc86d9a8c2f156b39e041ea629af28))
* give each MCP endpoint its own OAuth identity, and connect after serving ([51cb945](https://github.com/dreulavelle/mcpd/commit/51cb9450652a1fe8a226fb20402214a8caac8443))
* **logging:** both formats take turns at the one destination ([#56](https://github.com/dreulavelle/mcpd/issues/56)) ([b750850](https://github.com/dreulavelle/mcpd/commit/b750850486edc27b5ad8c81b67c8de65f781cf1f))
* **logs:** one record, one line, and the real message on it ([#64](https://github.com/dreulavelle/mcpd/issues/64)) ([2551cab](https://github.com/dreulavelle/mcpd/commit/2551cab72b6776f20bc0f56e1111ad6206467dd1))
* make tunnels from the plugin, and stop offering dead settings ([5ac76ad](https://github.com/dreulavelle/mcpd/commit/5ac76ad9cf6103c9ab8e01385aa71bfa36bedd75))
* **mcp:** do not advertise an authorization server that is not mounted ([c5877ce](https://github.com/dreulavelle/mcpd/commit/c5877ce5fad780bf55ab129b67bc56cf41e55ec0))
* **nav:** the keys page says which keys it means ([#55](https://github.com/dreulavelle/mcpd/issues/55)) ([d95408d](https://github.com/dreulavelle/mcpd/commit/d95408dfb17b9bfa68b692323df9e7d677e6579a))
* **observium:** a redirect means the wrong edition, and says so ([#73](https://github.com/dreulavelle/mcpd/issues/73)) ([bbdfb54](https://github.com/dreulavelle/mcpd/commit/bbdfb54423e026932a4f6ee83e1da7f8b7e6047b))
* **observium:** filters that matched nothing, against a real CE 26.1 ([#72](https://github.com/dreulavelle/mcpd/issues/72)) ([a21b135](https://github.com/dreulavelle/mcpd/commit/a21b13522030b99afb19526c1a6b52d2b7b67e9f))
* **plugins:** use a plugin's published schema; recover registration panics ([589ac87](https://github.com/dreulavelle/mcpd/commit/589ac877c2fe0ed3eb714d76adc2b6b3023ccc72))
* read-only is enforced where requests leave, not where paths are built ([#27](https://github.com/dreulavelle/mcpd/issues/27)) ([aa99446](https://github.com/dreulavelle/mcpd/commit/aa99446c0ab729ac049fe714ebed9af407e9a65f))
* roles read as "User" and "Admin" ([#8](https://github.com/dreulavelle/mcpd/issues/8)) ([88a7604](https://github.com/dreulavelle/mcpd/commit/88a76045629a172959370bedc5c1515ae7b8aad9))
* scope tunnel requests to an organization, and cut the page down ([0fa1e3e](https://github.com/dreulavelle/mcpd/commit/0fa1e3ecef5aa9ef9194711c2d4778f0067be4e8))
* serve https, because an OAuth issuer has to be one ([5b8f4ab](https://github.com/dreulavelle/mcpd/commit/5b8f4ab3b5a73bceeada7daa099c9a8d16c720a8))
* serve OAuth to ChatGPT connectors, and fix overlapping UI ([563487c](https://github.com/dreulavelle/mcpd/commit/563487c122b0ba37062bb94bb43f303ad114e1f2))
* settings roles, and rewrite the docs ([#6](https://github.com/dreulavelle/mcpd/issues/6)) ([501117b](https://github.com/dreulavelle/mcpd/commit/501117bd7fe0cbeaa039289b168c85a90a181e5e))
* **settings:** a conditional field pointed at a key its own form did not have ([#70](https://github.com/dreulavelle/mcpd/issues/70)) ([cc7798f](https://github.com/dreulavelle/mcpd/commit/cc7798fb031bf755641fa4d7586411df1256ce44))
* **sso:** a provider that is switched on either works or says why ([#60](https://github.com/dreulavelle/mcpd/issues/60)) ([ab3914c](https://github.com/dreulavelle/mcpd/commit/ab3914caf0ab539b036ac16668a18b12a88e365c))
* **sso:** the operator's own provider can actually be used ([#59](https://github.com/dreulavelle/mcpd/issues/59)) ([95d1624](https://github.com/dreulavelle/mcpd/commit/95d1624e0b67b922e2fdb13449a73525a19fd983))
* the role collapse left two invalid defaults behind ([#10](https://github.com/dreulavelle/mcpd/issues/10)) ([5bba202](https://github.com/dreulavelle/mcpd/commit/5bba202aa67176c13f78d41f6434e4f8aa4a4df5))
* the tunnel carries the connection, not the sign-in ([4c9d682](https://github.com/dreulavelle/mcpd/commit/4c9d68287e10dc584780131f4d80b92e3dc3e051))
* the tunnel settings ask for credentials, not a tunnel id ([#9](https://github.com/dreulavelle/mcpd/issues/9)) ([b8f0cd0](https://github.com/dreulavelle/mcpd/commit/b8f0cd026d48996c57de9223609abc5ce390421f))
* **tunnel:** make the settings form actually drive the tunnel ([bb46307](https://github.com/dreulavelle/mcpd/commit/bb46307e44a88f0a5dc777f393c03a2d6bb541bf))
* **web:** a tunnel id sits in its row rather than towering over it ([#20](https://github.com/dreulavelle/mcpd/issues/20)) ([cf7714d](https://github.com/dreulavelle/mcpd/commit/cf7714d686d5024fbc495f578deaa107ec2acbd3))


### Refactoring

* approvals belong in the conversation, so drop the Changes tab ([2715659](https://github.com/dreulavelle/mcpd/commit/2715659c65f172f153267a342a5acbf983727c51))
* **dashboard:** plainer words, fewer comments, and no half-empty grid ([#47](https://github.com/dreulavelle/mcpd/issues/47)) ([56ad2d9](https://github.com/dreulavelle/mcpd/commit/56ad2d9374d1ce7090eb789aaf03883399c27b03))
* **nav:** the administrative pages are siblings, not a drawer ([#58](https://github.com/dreulavelle/mcpd/issues/58)) ([30cec3b](https://github.com/dreulavelle/mcpd/commit/30cec3bdb61d037ece49b25270ff3574470f4247))
* **observium:** subscription only, and one code path again ([#76](https://github.com/dreulavelle/mcpd/issues/76)) ([ab44573](https://github.com/dreulavelle/mcpd/commit/ab445735abbee8deb65136db580e120af4e684c9))
* **ui:** plain language and a proper design pass ([032a41c](https://github.com/dreulavelle/mcpd/commit/032a41c28c938533846a1882485fa00d659bbd7b))
* **web:** a page per job, and tunnels you can actually make ([aba07aa](https://github.com/dreulavelle/mcpd/commit/aba07aab2077799c488d25d1323f1bddc9da1b81))
* **web:** one frame for signing in, one shape for a page ([#14](https://github.com/dreulavelle/mcpd/issues/14)) ([12bff26](https://github.com/dreulavelle/mcpd/commit/12bff266e6c794fb1fd84abba522a2fd904fc59b))


### Documentation

* **cnmaestro:** say what gates the API Clients page ([#19](https://github.com/dreulavelle/mcpd/issues/19)) ([470518e](https://github.com/dreulavelle/mcpd/commit/470518e305c53fe8b4b5e09181765448cfa1f842))
* revise cnMaestro findings against API 6.3.0; add endpoint deny-list ([10fd6cb](https://github.com/dreulavelle/mcpd/commit/10fd6cba858b72bb9f36c54d0c610dd7f5a7909b))
* say which address ChatGPT actually uses ([e66e1c3](https://github.com/dreulavelle/mcpd/commit/e66e1c3c0a9e7782893509c9f5b4e8ce34d10b09))
* the README describes the host as it is now ([#3](https://github.com/dreulavelle/mcpd/issues/3)) ([036e99d](https://github.com/dreulavelle/mcpd/commit/036e99deca76f5633b24d60c3eeaa112d7e7cbba))
