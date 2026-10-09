# Changelog

## [0.1.1](https://github.com/openfga/mapper/compare/v0.1.0...v0.1.1) (2026-10-09)


### Fixed

* **apply:** patch filters with type-prefix objects (e.g. `org:`) now correctly scope desired tuples by prefix match rather than exact equality, consistent with how FGA Read interprets the same filter; previously this caused a silent mass delete of all matching tuples, or from v0.1.0 a hard coverage error on every matching event ([043802d](https://github.com/openfga/mapper/commit/043802d49f7a3ca4e84245d6de9bfa302d340f0c))

## 0.1.0 (2026-10-08)


### Added

* add Compact function for cross-record tuple dedup and conflict detection ([#13](https://github.com/openfga/mapper/issues/13)) ([64ae7f4](https://github.com/openfga/mapper/commit/64ae7f46a68740864d579206a3b54b4e0c8e1364))
* add FGA mapping engine ([#3](https://github.com/openfga/mapper/issues/3)) ([2d8d8dd](https://github.com/openfga/mapper/commit/2d8d8dd50bd67c804e5492226818a48daa24a0ed))
* add mapping language parser and validator ([#2](https://github.com/openfga/mapper/issues/2)) ([9d9e977](https://github.com/openfga/mapper/commit/9d9e977cb1ef45d582fdf6c2c038e24f0929dd70))
* **apply:** add package for applying mapping results to a store ([#17](https://github.com/openfga/mapper/issues/17)) ([35a804a](https://github.com/openfga/mapper/commit/35a804af88e0f96767a810816106ed98acfeff72))


### Fixed

* **language:** require an object on every tuple filter ([#8](https://github.com/openfga/mapper/issues/8)) ([ece3bca](https://github.com/openfga/mapper/commit/ece3bcae5b5f7f388d057f8afa7955c1ab4839e5))


### Documentation

* update READMEs for apply package ([#18](https://github.com/openfga/mapper/issues/18)) ([9860017](https://github.com/openfga/mapper/commit/986001722cfc99b59780b62de153dcdb811a3d85))
