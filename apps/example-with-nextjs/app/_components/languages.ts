// Language presets for the playground. `language`/`version`/`file` map straight
// onto the code-runner manifests in /languages; `monaco` is the editor's syntax
// mode. The snippets exercise stdout AND interactive stdin where supported.

export interface LanguagePreset {
  id: string;
  label: string;
  language: string;
  version: string;
  file: string;
  monaco: string;
  snippet: string;
}

export const LANGUAGES: LanguagePreset[] = [
  {
    id: "python-3.12",
    label: "Python 3.12",
    language: "python",
    version: "3.12",
    file: "main.py",
    monaco: "python",
    snippet: `print("what is your name?")
name = input()
print(f"hello, {name}!")

for i in range(3):
    print(f"line {i}")
`,
  },
  {
    id: "python-3.12-plot",
    label: "Python · matplotlib (artifact)",
    language: "python",
    version: "3.12",
    file: "main.py",
    monaco: "python",
    snippet: `import numpy as np
import matplotlib.pyplot as plt

# code-runner auto-captures open matplotlib figures as workspace artifacts —
# no savefig() needed. The figure is uploaded and returned as a presigned URL.
x = np.linspace(0, 2 * np.pi, 400)
plt.figure(figsize=(6, 3))
plt.plot(x, np.sin(x), label="sin")
plt.plot(x, np.cos(x), label="cos")
plt.title("sin & cos")
plt.legend()
plt.grid(True, alpha=0.3)

print("rendered a figure — see the Artifacts panel below")
`,
  },
  {
    id: "r-4.4",
    label: "R 4.4",
    language: "r",
    version: "4.4",
    file: "main.R",
    monaco: "r",
    snippet: `cat("what is your name?\\n")
name <- readLines("stdin", n = 1)
cat(sprintf("hello, %s!\\n", name))

print(summary(c(1, 2, 3, 4, 5)))
`,
  },
  {
    id: "rust-1.83",
    label: "Rust 1.83",
    language: "rust",
    version: "1.83",
    file: "main.rs",
    monaco: "rust",
    snippet: `use std::io::{self, BufRead, Write};

fn main() {
    println!("what is your name?");
    io::stdout().flush().unwrap();

    let mut name = String::new();
    io::stdin().lock().read_line(&mut name).unwrap();
    println!("hello, {}!", name.trim());
}
`,
  },
  {
    id: "c-14",
    label: "C (GCC 14)",
    language: "c",
    version: "14",
    file: "main.c",
    monaco: "c",
    snippet: `#include <stdio.h>

int main(void) {
    char name[128];
    printf("what is your name?\\n");
    if (scanf("%127s", name) == 1) {
        printf("hello, %s!\\n", name);
    }
    return 0;
}
`,
  },
  {
    id: "cpp-14",
    label: "C++ (G++ 14)",
    language: "cpp",
    version: "14",
    file: "main.cpp",
    monaco: "cpp",
    snippet: `#include <iostream>
#include <string>

int main() {
    std::cout << "what is your name?" << std::endl;
    std::string name;
    std::getline(std::cin, name);
    std::cout << "hello, " << name << "!" << std::endl;
}
`,
  },
  {
    id: "sqlite-3",
    label: "SQLite 3",
    language: "sqlite",
    version: "3",
    file: "main.sql",
    monaco: "sql",
    snippet: `create table langs (name text, year int);
insert into langs values ('python', 1991), ('rust', 2010);
select name, year from langs order by year;
`,
  },
];
