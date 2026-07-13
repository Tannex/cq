# COBOL Copybook Test Fixtures - cb2xml Source Collection

This directory contains unmodified COBOL copybooks and expected XML outputs
from the [bmTas/cb2xml](https://github.com/bmTas/cb2xml) open-source
repository. They are used as test fixtures to validate COBOL parsing.

The files were verified byte-for-byte against upstream commit
[`f71bf3be2ec720be8b0154475226aaba575a6311`](https://github.com/bmTas/cb2xml/tree/f71bf3be2ec720be8b0154475226aaba575a6311).
Copyright and authorship remain with the cb2xml contributors.

## License

The upstream project distributes these files under the **GNU Lesser General
Public License v2.1 (LGPL-2.1)**. The complete upstream license text is
included in [`LICENSE`](LICENSE). This notice does not change the license of
the rest of cq, which is covered by the repository-root `LICENSE`.

## Copybook Files and Features

### Basic Examples

#### DTAR107.cbl
- **Source**: examples/PythonExample/DTAR107.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/examples/PythonExample/DTAR107.cbl
- **Expected XML**: expected-xml/DTAR107.cbl.xml
- **Features**: COMP-3 (packed decimal), REDEFINES clause, level-88 condition names (VALUE, VALUES), signed numeric fields (S9), decimal point handling (V99)
- **Description**: Customer file record with multiple COMP-3 packed decimal fields, field redefinition, and condition names for transaction types

#### DTAR119.cbl
- **Source**: examples/PythonExample/DTAR119.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/examples/PythonExample/DTAR119.cbl
- **Expected XML**: expected-xml/DTAR119.cbl.xml
- **Features**: COMP-3 (packed decimal), signed numeric (S9), unsigned numeric (9), decimal scaling (V99)
- **Description**: Transaction summary record with mixed signed and unsigned COMP-3 fields with decimal places

#### Vendor.cbl
- **Source**: source/cb2xml_examples/src/net/sf/cb2xml/example/Vendor.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/source/cb2xml_examples/src/net/sf/cb2xml/example/Vendor.cbl
- **Features**: Nested group structure, level-88 condition names with THRU ranges, numeric and alphanumeric PIC clauses
- **Description**: Vendor record with location and address details, demonstrates nested group hierarchy and range-based conditions

### COMP (Binary) Field Tests

#### cpyComp.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyComp.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyComp.cbl
- **Expected XML**: expected-xml/cb2xml_Output104.xml (related test output)
- **Features**: COMP (binary) usage, signed numeric (S9), variable precision, decimal scaling (V99), multiple field sizes
- **Description**: Comprehensive test of COMP binary numeric fields with varying sizes and decimal positions

#### cpyComp5.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyComp5.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyComp5.cbl
- **Expected XML**: expected-xml/cb2xml_Output106.xml (related test output)
- **Features**: COMP-5 (binary) usage, signed numeric fields, full word alignment, decimal scaling
- **Description**: COMP-5 binary numeric test with variable field sizes and precision

### COMP-3 (Packed Decimal) Field Tests

#### cpyComp3.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyComp3.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyComp3.cbl
- **Expected XML**: expected-xml/cb2xml_Output103.xml (related test output)
- **Features**: COMP-3 (packed decimal) usage, signed numeric (S9), variable precision (up to 15 digits), decimal scaling (V99)
- **Description**: Comprehensive packed decimal field test with a range of field sizes and decimal positions

#### cpyComp3Sync.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyComp3Sync.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyComp3Sync.cbl
- **Expected XML**: expected-xml/cb2xml_Output107.xml (related test output)
- **Features**: COMP-3 with SYNC clause, signed numeric, boundary alignment, decimal scaling
- **Description**: COMP-3 packed decimal fields with SYNC (synchronization) clauses for memory alignment

#### cpyCompSync.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyCompSync.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyCompSync.cbl
- **Expected XML**: expected-xml/cb2xml_Output105.xml (related test output)
- **Features**: COMP with SYNC clause, signed numeric fields, boundary synchronization, decimal positions
- **Description**: COMP binary fields with SYNC for memory-aligned storage

### OCCURS Tests (Array/Table Structures)

#### cpyOccurs.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyOccurs.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyOccurs.cbl
- **Expected XML**: expected-xml/cb2xml_Output108.xml (related test output)
- **Features**: OCCURS clause with fixed repetitions, level-88 condition names
- **Description**: Simple fixed-size table with array elements and condition name definition

#### cpyOccursDepending.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyOccursDepending.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyOccursDepending.cbl
- **Expected XML**: expected-xml/cb2xml_Output109.xml (related test output)
- **Features**: OCCURS DEPENDING ON clause, variable table size, signed numeric fields
- **Description**: Table with size controlled by a separate field (dynamic array), multiple variable-length tables

#### cpyOccursDependingOn21.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyOccursDependingOn21.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyOccursDependingOn21.cbl
- **Expected XML**: expected-xml/cb2xml_Output110.xml (related test output)
- **Features**: Nested OCCURS clauses, OCCURS DEPENDING ON, fixed and variable-length arrays
- **Description**: Complex nested array structure with one OCCURS clause depending on a field, and another fixed OCCURS clause within it

### REDEFINES Tests (Field Overlays)

#### cpyRedefSize.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyRedefSize.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyRedefSize.cbl
- **Expected XML**: expected-xml/cb2xml_Output111.xml (related test output)
- **Features**: REDEFINES clause, OCCURS within redefining group, field overlay with different layouts
- **Description**: Field overlay demonstrating REDEFINES with OCCURS to provide alternative field interpretations

#### cpyRedefSize01.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyRedefSize01.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyRedefSize01.cbl
- **Expected XML**: expected-xml/cb2xml_Output112.xml (related test output)
- **Features**: Multiple REDEFINES clauses on same base field, different redefining structures
- **Description**: Multiple alternative field layouts overlaying the same base record area

### Level-88 Condition Names Tests

#### Test_88.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/Test_88.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/Test_88.cbl
- **Expected XML**: expected-xml/cbl2xml_Test102.xml (related test output)
- **Features**: Level-88 condition names with VALUE, VALUES (single and multiple), THRU ranges, mixed single/range conditions
- **Description**: Comprehensive test of level-88 condition naming with various condition types and value specifications

#### cpyComp3_88a.cbl
- **Source**: src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyComp3_88a.cbl
- **URL**: https://raw.githubusercontent.com/bmTas/cb2xml/master/src/test/resources/net/sf/cb2xml/zTests/common/cobolCopybook/cpyComp3_88a.cbl
- **Expected XML**: expected-xml/cb2xml_Output102.xml (related test output)
- **Features**: COMP-3 with level-88 condition names, signed numeric, decimal scaling, range-based conditions (THRU)
- **Description**: COMP-3 packed decimal record with level-88 conditions on numeric fields with decimal ranges

## Feature Coverage Summary

| Feature | Files | Count |
|---------|-------|-------|
| COMP-3 (Packed Decimal) | cpyComp3.cbl, cpyComp3Sync.cbl, cpyComp3_88a.cbl, DTAR107.cbl, DTAR119.cbl | 5 |
| COMP (Binary) | cpyComp.cbl, cpyComp5.cbl, cpyCompSync.cbl | 3 |
| SYNC Clause | cpyComp3Sync.cbl, cpyCompSync.cbl | 2 |
| OCCURS | cpyOccurs.cbl, cpyRedefSize.cbl, cpyRedefSize01.cbl | 3 |
| OCCURS DEPENDING ON | cpyOccursDepending.cbl, cpyOccursDependingOn21.cbl | 2 |
| REDEFINES | cpyRedefSize.cbl, cpyRedefSize01.cbl, DTAR107.cbl | 3 |
| Level-88 Condition Names | Test_88.cbl, cpyComp3_88a.cbl, Vendor.cbl, DTAR107.cbl, cpyOccurs.cbl | 5 |
| Signed Numeric (S9) | All COMP/COMP-3 files + DTAR107, DTAR119 | 10 |
| Decimal Scaling (V99) | cpyComp.cbl, cpyComp3.cbl, cpyComp5.cbl, cpyCompSync.cbl, cpyComp3Sync.cbl, DTAR107.cbl, DTAR119.cbl | 7 |
| Nested Groups | Vendor.cbl | 1 |

## Expected XML Files

Corresponding expected XML output files are provided in the `expected-xml/` subdirectory:

- **DTAR107.cbl.xml** - Expected output for DTAR107.cbl
- **DTAR119.cbl.xml** - Expected output for DTAR119.cbl
- **cb2xml_Output102-112.xml** - Expected outputs for various test copybooks

These XML files contain the expected parsed field structure, including:
- Field names and hierarchy levels
- Display length and storage length
- Numeric/signed/picture information
- COMP/COMP-3/COMP-5 usage types
- SYNC clause effects
- OCCURS clause parameters
- REDEFINES relationships
- Level-88 condition definitions

## File Naming Convention

- **\*.cbl** - COBOL copybook source files
- **expected-xml/\*.xml** - Expected parsed XML output from cb2xml parser

## Repository Information

- **Repository**: https://github.com/bmTas/cb2xml
- **Default Branch**: master
- **License**: GNU Lesser General Public License v2.1 (LGPL-2.1)
- **Source Revision**: f71bf3be2ec720be8b0154475226aaba575a6311 (2026-07-07)

## Usage Notes

These copybooks are designed to test:
1. COBOL syntax parsing correctness
2. Field position and length calculation
3. Numeric type handling (COMP, COMP-3, COMP-5)
4. Memory alignment (SYNC clause)
5. Dynamic array structures (OCCURS DEPENDING ON)
6. Field overlays (REDEFINES)
7. Condition name resolution (level-88)
8. Decimal and fractional numeric handling
9. Signed and unsigned numeric fields
10. Nested group structures

The expected XML output files can be used as cross-validation for parser implementations.
