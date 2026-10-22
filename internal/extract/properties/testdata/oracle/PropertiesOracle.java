import java.io.FileInputStream;
import java.io.InputStreamReader;
import java.io.Reader;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Properties;

// Prints, for each file named, one line of JSON: every key and value
// Properties.load reads from it as UTF-8, in order, or the refusal.
// TestRegenerateOracleCorpora runs it; see ini_oracle_test.go.
public class PropertiesOracle {
    public static void main(String[] args) throws Exception {
        StringBuilder out = new StringBuilder();
        for (String file : args) {
            List<String[]> got = new ArrayList<>();
            Properties p = new Properties() {
                @Override
                public synchronized Object put(Object k, Object v) {
                    got.add(new String[] {(String) k, (String) v});
                    return null;
                }
            };
            try (Reader r = new InputStreamReader(new FileInputStream(file), StandardCharsets.UTF_8)) {
                p.load(r);
                out.append("{\"entries\":[");
                for (int i = 0; i < got.size(); i++) {
                    if (i > 0) out.append(',');
                    out.append('[').append(json(got.get(i)[0])).append(',').append(json(got.get(i)[1])).append(']');
                }
                out.append("]}\n");
            } catch (IllegalArgumentException e) {
                out.append("{\"refused\":").append(json(e.getMessage())).append("}\n");
            }
        }
        System.out.print(out);
    }

    static String json(String s) {
        StringBuilder b = new StringBuilder("\"");
        for (char c : s.toCharArray()) {
            if (c == '"' || c == '\\') b.append('\\').append(c);
            else if (c < 0x20 || c > 0x7e) b.append(String.format("\\u%04x", (int) c));
            else b.append(c);
        }
        return b.append('"').toString();
    }
}
