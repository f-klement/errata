<?php
// Prints, for each file named, one line of JSON: the sections, keys and
// values PHP's parse_ini_string reads from it in normal mode, the mode PHP
// reads php.ini in, with the keys before the first section under "", or
// the refusal. A key set twice keeps its last value, and key[] an array.
// TestRegenerateOracleCorpora runs it; see ini_oracle_test.go.
foreach (array_slice($argv, 1) as $file) {
    error_clear_last();
    $read = @parse_ini_string(file_get_contents($file), true, INI_SCANNER_NORMAL);
    if ($read === false) {
        echo json_encode(["refused" => error_get_last()["message"] ?? "false"]), "\n";
        continue;
    }
    $sections = [];
    foreach ($read as $name => $value) {
        if (is_array($value) && $name !== "arr") { // arr is the one key[] the generator writes
            $sections[(string)$name] = $value;
        } else {
            $sections[""][(string)$name] = $value;
        }
    }
    echo json_encode(["sections" => $sections], JSON_FORCE_OBJECT | JSON_INVALID_UTF8_SUBSTITUTE), "\n";
}
