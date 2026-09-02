# Changelog

## [0.3.0](https://github.com/pietervanleuven/rehost/compare/v0.2.1...v0.3.0) (2026-09-02)


### Features

* **recipe/laravel:** add the Laravel recipe ([86f1703](https://github.com/pietervanleuven/rehost/commit/86f1703517fdf45c9ccc16cbc809005695f2695a))
* **recipe/laravel:** add the Laravel recipe ([5bfc215](https://github.com/pietervanleuven/rehost/commit/5bfc2151cf79d93a7ba17b514c73d042b4931886))

## [0.2.1](https://github.com/pietervanleuven/rehost/compare/v0.2.0...v0.2.1) (2026-08-30)


### Bug Fixes

* **cli:** wait for the DEFINER stripper before closing the dump reader ([c6b691e](https://github.com/pietervanleuven/rehost/commit/c6b691ebd953660f37643e9e33a1d75f4fef49f3))
* **project:** refuse sites that share a destination ([0387307](https://github.com/pietervanleuven/rehost/commit/03873074d88fb78292ee33c2834638f2090db45c))

## [0.2.0](https://github.com/pietervanleuven/rehost/compare/v0.1.0...v0.2.0) (2026-08-29)


### Features

* **build:** publish a Homebrew cask on every release ([1562b6e](https://github.com/pietervanleuven/rehost/commit/1562b6edc9bd56aef1937c4afce62af98fc2e4c0))
* **build:** publish a Homebrew cask on every release ([37a733c](https://github.com/pietervanleuven/rehost/commit/37a733c788a8e4f435857d1f21f697d83a3a3123))
* **cli,project:** drive dump/import selection by database driver ([6121cef](https://github.com/pietervanleuven/rehost/commit/6121cef067bd95b5ef3ff283a0f432ffd34631dd))
* **db,ssh,check:** MariaDB-named tooling, driver plumbing, engine advice ([de8d794](https://github.com/pietervanleuven/rehost/commit/de8d794a3e47bb18953be7ba7366f9499f14b72b))
* **recipe:** Joomla, PrestaShop and Craft CMS recipes ([30593e0](https://github.com/pietervanleuven/rehost/commit/30593e01dd09a381a6a21c5972ce9dfd4ff77336))


### Bug Fixes

* **check,recipe,cli:** multisite refusal, honest disk math, exit codes ([ccf2e7b](https://github.com/pietervanleuven/rehost/commit/ccf2e7b7962d90964ea647de98e2da68b9890f11))
* **check:** parse the MySQL version, not the distro package revision ([af3392d](https://github.com/pietervanleuven/rehost/commit/af3392de78d42aa7c22a4efbaa818bfb0737cbac))
* **cli,state:** run lock, gate-before-prompt, non-interactive DB password ([28c92f4](https://github.com/pietervanleuven/rehost/commit/28c92f451eec21a664799c5f08286320af982e9c))
* **cli,transfer,recipe:** close the remote-controlled path and filename holes ([0bef4ce](https://github.com/pietervanleuven/rehost/commit/0bef4ce9b576b3af7e8d1f1eebdea1aaa5122b86))
* **db,check,cli:** name the actual engine in report rows ([d4a5277](https://github.com/pietervanleuven/rehost/commit/d4a52779e53f6be9178c50603e7646111c035c2b))
* **db,recipe,cli:** credential and charset correctness minors ([22f6442](https://github.com/pietervanleuven/rehost/commit/22f6442b05eefcd5da0278c4dfc9cbc9c21bd54d))
* **db:** bind the dump heredoc to mysqldump, not gzip ([33aa6b6](https://github.com/pietervanleuven/rehost/commit/33aa6b6a3dee80ba723ce4366fd551df3b12e405))
* **dns,check,cli,tui:** make the cutover advice honest ([6706792](https://github.com/pietervanleuven/rehost/commit/6706792d14b8c11a8316a851acc858c99f4c0145))
* **recipe:** scope Drupal rewrites to $databases and mask WP comments ([3ff7b9f](https://github.com/pietervanleuven/rehost/commit/3ff7b9ff7f5d83485e76e2a94b5bafcfc5a797b3))
* **searchreplace,detect,cli,tui:** three robustness majors from the review ([11d0db3](https://github.com/pietervanleuven/rehost/commit/11d0db316f8682140e2d5dee8dcd66dba709cbf3))
* **searchreplace:** opaque comments/idents in dumps, more URL variants ([d2cf3e6](https://github.com/pietervanleuven/rehost/commit/d2cf3e6503b075980e1761a3f62bb5c9982cf407))
* **ssh,cli:** probe fallback honesty, IPv6 targets, ShellQuote tests ([4e01af9](https://github.com/pietervanleuven/rehost/commit/4e01af935362db04eb1bbf734ad98761f03eec0d))
* **ssh:** host key algorithm hinting, keepalives, bounded cancel wait ([b3f3c2f](https://github.com/pietervanleuven/rehost/commit/b3f3c2f2b122456fc526dd5fcaae70236950e0ad))
* **transfer:** carry symlinks and repair interrupted degraded transfers ([91439ce](https://github.com/pietervanleuven/rehost/commit/91439ceb01331415453de3c66cf9255e5bbaa9af))

## 0.1.0 (2026-08-27)


### ⚠ BREAKING CHANGES

* **tui:** the plan JSON schema renames from rehost.capability-report.v1 to rehost.plan-report.v2 and the separate rehost.dryrun-report.v1 document no longer exists. No released consumers.

### Features

* **check:** add compatibility gate between source and destination ([3ca501e](https://github.com/pietervanleuven/rehost/commit/3ca501efa34f2bc64fd432950bbe41f4f6b01dda))
* **check:** warn when DNS TTLs are too high for a fast cutover ([ac66c3a](https://github.com/pietervanleuven/rehost/commit/ac66c3a4d4a049a3c6efc0ba84aa3f52baa78e29))
* **cli,tui:** rehost cutover - verified go-live checklist; migrate exits 0 ([9d1e45e](https://github.com/pietervanleuven/rehost/commit/9d1e45e7c0f3068dfd01c1cb774caae9095e1be6))
* **cli:** add cobra skeleton with stub commands and version ([56dd6fc](https://github.com/pietervanleuven/rehost/commit/56dd6fcf4abd418e8a3d40984fa519885f863c21))
* **cli:** add init wizard with per-host connectivity test ([22f605a](https://github.com/pietervanleuven/rehost/commit/22f605aa780ad6bd5b16722618882046f7d88b63))
* **cli:** add plan --dry-run with verified DB dump and throughput sample ([3551200](https://github.com/pietervanleuven/rehost/commit/35512001c387ee1f01b5559644fbcbc49a016484))
* **cli:** ask for a host interactively when plan has no target ([0c4c3d2](https://github.com/pietervanleuven/rehost/commit/0c4c3d2b2efa5123923409c798f28469e02e8d2c))
* **cli:** discover sites recursively in plan, add --docroot flag ([4e01941](https://github.com/pietervanleuven/rehost/commit/4e01941386b72a89ee0f5b96e47e4c695369ed8e))
* **cli:** implement read-only status and history commands ([58f664e](https://github.com/pietervanleuven/rehost/commit/58f664e0f3cefd6860ece61666c724834c62f21d))
* **cli:** migrate pre-flight with destination-state policy enforcement ([c090988](https://github.com/pietervanleuven/rehost/commit/c0909888e93a162c952d64155b6b0622932dc421))
* **cli:** report detected frameworks in plan ([1b0d4c3](https://github.com/pietervanleuven/rehost/commit/1b0d4c33377fc3b222857f05ea59b8af79f69f16))
* **cli:** report plan progress in stages ([55868f6](https://github.com/pietervanleuven/rehost/commit/55868f6a574236037fa38e5259b1b7b7b20da9ea))
* **cli:** show user@host in reports and progress ([39e0c4f](https://github.com/pietervanleuven/rehost/commit/39e0c4f38f7ac340e50ff47330eeaf86ddee6e03))
* **cli:** wire plan command to connect and print capability report ([5fd32a3](https://github.com/pietervanleuven/rehost/commit/5fd32a3bdc7170ba4dea09cc25a9896bd56edb6d))
* **cli:** wire the database choreography into migrate ([77e41ee](https://github.com/pietervanleuven/rehost/commit/77e41ee8c985ae4d1ff71de3f830aff03f671f16))
* **cli:** wire the sync engine into migrate - green pre-flight syncs ([5b559e0](https://github.com/pietervanleuven/rehost/commit/5b559e0df98ad81da29f951987179bf4ba9dd80c))
* **db:** dump databases via a PHP helper when mysqldump is missing ([2ce7e26](https://github.com/pietervanleuven/rehost/commit/2ce7e26c3f41a3fc9d9c43e7ccc33f46febda14a))
* **db:** dump views, triggers and routines in the PHP fallback ([76da1af](https://github.com/pietervanleuven/rehost/commit/76da1af321dbb0f942b5a453ae868d164919403f))
* **db:** extract source database credentials in layers ([79203cd](https://github.com/pietervanleuven/rehost/commit/79203cdc4a370c38b04c6f1edc9e0e0d7147e547))
* **db:** inspect source databases and gate on connectivity and charset ([1acbecb](https://github.com/pietervanleuven/rehost/commit/1acbecb89ee7eef59aceb1fbd6e7b39a82f2770d))
* **db:** stream the dump into the destination MySQL with progress ([3bf4af3](https://github.com/pietervanleuven/rehost/commit/3bf4af3d7b2384d2df8615dfb12dd213ce5ffcbf))
* **detect:** add remote filesystem abstraction and scan engine ([92089ee](https://github.com/pietervanleuven/rehost/commit/92089eeddd32657a527f1424220a9fd83724dc6f))
* **detect:** de-duplicate sites by canonical path ([099b1e2](https://github.com/pietervanleuven/rehost/commit/099b1e2537a388f39b49a3dee06ae88bf15c4d16))
* **detect:** discover sites recursively with find and a walk fallback ([11d5e8e](https://github.com/pietervanleuven/rehost/commit/11d5e8e3d67819d339ccea9b7ec4a8ca8ab31c8d))
* **dns:** snapshot domain records and warn when mail points at the source ([0cb54a4](https://github.com/pietervanleuven/rehost/commit/0cb54a49d19a2701ffda9f4f5b0d4cb30c927079))
* **inventory:** measure site sizes with suggested excludes, persist sites from plan ([4de6bef](https://github.com/pietervanleuven/rehost/commit/4de6befb08d7654818d299a391c9def1e59bcd16))
* **project:** add migrate.yaml schema v1 with load and save ([ce8f870](https://github.com/pietervanleuven/rehost/commit/ce8f8701a101468ddb79ea9d71d11bfac13e1122))
* **project:** comment-preserving migrate.yaml writes ([fecf453](https://github.com/pietervanleuven/rehost/commit/fecf4531d4ed3fb437f30fda3cc7baedbc8ed827))
* **project:** comment-preserving migrate.yaml writes ([7f730fd](https://github.com/pietervanleuven/rehost/commit/7f730fda46512eda4ea9a42a37a25e6e77b9da0d))
* **project:** per-site dest_db block naming the destination database ([889d385](https://github.com/pietervanleuven/rehost/commit/889d385912e491e0bfb4a39723e6b16471c4f3f6))
* **recipe,cli:** config rewrite - point the migrated site at its new database ([d8bcfcf](https://github.com/pietervanleuven/rehost/commit/d8bcfcf7f5a1ace28364926b38c4b8916fa1d773))
* **recipe,cli:** maintenance-mode strategies and the unlock command ([47fb090](https://github.com/pietervanleuven/rehost/commit/47fb0900140d0104894c8ee453912c19d1dbdad3))
* **recipe:** add drupal, wordpress and static detection recipes ([8efde41](https://github.com/pietervanleuven/rehost/commit/8efde41512f2654d9c60ecef3f4a532cb1821d27))
* **recipe:** direct-database fallback for Drupal maintenance mode ([527cb31](https://github.com/pietervanleuven/rehost/commit/527cb31e7c57f5195a60fe798fbc61ac8655009e))
* **searchreplace:** serialized-safe replacement core with fuzzing ([924d373](https://github.com/pietervanleuven/rehost/commit/924d373f63e280a7187219606fc100c2dd14b1ca))
* **searchreplace:** serialized-safe rewriting of a SQL dump stream ([0cd5323](https://github.com/pietervanleuven/rehost/commit/0cd53231ec9aa0c58a84059a5af025d1462d0da3))
* **ssh:** add connection layer with agent, key and password auth ([26aa6a2](https://github.com/pietervanleuven/rehost/commit/26aa6a2c2562d8fcb985b088873de80c3a17bda5))
* **ssh:** add remote capability probe ([24d7224](https://github.com/pietervanleuven/rehost/commit/24d72244309a5704e7a5600e1cdbb1f58b31a969))
* **state:** bound history.jsonl growth with semantic-safe compaction ([4572cca](https://github.com/pietervanleuven/rehost/commit/4572cca6c70f482d1d90949aab765f781f50d736))
* **state:** bound history.jsonl growth with semantic-safe compaction ([06a3096](https://github.com/pietervanleuven/rehost/commit/06a3096e93ca760e6061b6e5366816f9779f5998))
* **state:** record run history in a hidden folder on the source ([d552721](https://github.com/pietervanleuven/rehost/commit/d5527218314beb00de3689128723adcaac564164))
* **transfer:** manifest-driven tar-pipe sync engine over the relay ([5520863](https://github.com/pietervanleuven/rehost/commit/5520863b26ae7b263bb72f139e56f27ec3c9a81b))
* **transfer:** take file manifests and report deltas between dry runs ([f82260d](https://github.com/pietervanleuven/rehost/commit/f82260dc8ff35c53bae6376d5b54d7bd50d51146))
* **tui:** add styled, plain and json capability report renderers ([0030d62](https://github.com/pietervanleuven/rehost/commit/0030d6267795a14351bd42ea00b1ec8317280388))
* **tui:** merge plan and dry-run JSON into one plan-report.v2 envelope ([3ea9c1f](https://github.com/pietervanleuven/rehost/commit/3ea9c1f62b730681ed04f8b77f336bfac93bf07f))


### Bug Fixes

* **check:** block un-dumpable source and stop nested-install disk double-count ([05a3af4](https://github.com/pietervanleuven/rehost/commit/05a3af4d46d619c66928c6078640c614dc25d0b9))
* **check:** tell the truth about transfer strategy and dest_db ([f84bde5](https://github.com/pietervanleuven/rehost/commit/f84bde5617a4c74ce652fe8e555d7a765ce4a778))
* **cli:** close the destination-policy and idempotency gaps in migrate ([0dc1f25](https://github.com/pietervanleuven/rehost/commit/0dc1f25d04548f60afd3d26e862b1649f6e928b8))
* **cli:** document the real flow order and add next-step hints ([298c8a5](https://github.com/pietervanleuven/rehost/commit/298c8a5e9e8f4aa834d72d8a2a64bf30b2b82df8))
* **cli:** harden the database and maintenance choreography ([0c50983](https://github.com/pietervanleuven/rehost/commit/0c5098310c626d08bc7cef220129c2f007c5f2eb))
* **cli:** truthful status, cancellable commands, honest global flags ([15c838a](https://github.com/pietervanleuven/rehost/commit/15c838ac795d82b16c173ea70396dac78ede6212))
* **db:** abort the PHP dump when a result stream dies mid-fetch ([77c82e0](https://github.com/pietervanleuven/rehost/commit/77c82e01cdf497e360ac92a404b52264e2dc7306))
* **db:** anchor dump footer check and count CREATE TABLE across chunks ([161af94](https://github.com/pietervanleuven/rehost/commit/161af94b5e737da90ef489ce43e8da3f08c89da4))
* **inventory:** apply manifest-grade rigor to size and throughput sampling ([4ac5595](https://github.com/pietervanleuven/rehost/commit/4ac5595fbf252138451a52323e55101a62ca4c21))
* **lint:** make the whole repo golangci-lint clean ([16cab49](https://github.com/pietervanleuven/rehost/commit/16cab49f6357232bebf0a1fbecab1f59ad38233e))
* make the suite pass on Windows runners ([8be07f4](https://github.com/pietervanleuven/rehost/commit/8be07f42f977a1bcd6576d48f35f8e32f14e6161))
* **project:** preserve dest_db/dest_root on plan rerun ([71d4a2b](https://github.com/pietervanleuven/rehost/commit/71d4a2b2ceff8afa922214815a55de6b24cc9f17))
* **recipe/drupal:** ignore settings.php doc-comment when reading DB config ([40da8de](https://github.com/pietervanleuven/rehost/commit/40da8de3422da18383440e6902a6c55b7b030c1a))
* **recipe/drupal:** stop reporting a Drupal 8+ core/ dir as a site ([af7737a](https://github.com/pietervanleuven/rehost/commit/af7737a1c04b12ee233a345e3076feca40345cd5))
* **recipe/wordpress:** keep maintenance mode from lifting or leaking ([235a6b5](https://github.com/pietervanleuven/rehost/commit/235a6b57ffbca49ada20f7016a5673ee3341e6ac))
* **searchreplace:** guard serialized length headers against int overflow ([daa65dc](https://github.com/pietervanleuven/rehost/commit/daa65dc50807525dfbaf228fbebb59cb51dbbee0))
* **ssh:** release the ssh-agent socket once the handshake is done ([b8674de](https://github.com/pietervanleuven/rehost/commit/b8674de806d53e9692174468925dd158726ab7e8))
* **ssh:** tolerate malformed known_hosts lines like openssh ([9f769b2](https://github.com/pietervanleuven/rehost/commit/9f769b2ebbbfdee378f2aa2057673ce4b30905f0))
* **transfer:** harden manifest capture against truncation and odd filenames ([79c397c](https://github.com/pietervanleuven/rehost/commit/79c397c9890b817772fd3f230b215fa14554d91e))
* **transfer:** stop trusting incomplete listings and masked tar exits ([0598746](https://github.com/pietervanleuven/rehost/commit/05987462df6c3f2639e0112b6e9e4fc1583641f5))
* truthful check gate, coherent flow, and code-review hardening ([c3788dc](https://github.com/pietervanleuven/rehost/commit/c3788dcb751a90c68dac41964c932e64e8367054))
