#!/bin/bash

# Define the YAML file path
yaml_file=$1
output_file=$2

# Create a temporary directory to store the split sections
mkdir -p temp_yaml_sections

# Initialize variables for section counting and flag to track changes
section_count=0
in_section=false

# Iterate through the YAML file
while IFS= read -r line; do
    if [[ $line == '---' ]]; then
        # Start a new section
        in_section=true
        section_count=$((section_count + 1))
        current_section_file="temp_yaml_sections/section_${section_count}.yaml"
        continue
    fi

    if [[ $in_section == true ]]; then
        # Replace "shard-key=" with "shard-key='shard1'"
        if [[ $line == *"shard-key"* ]]; then
            line=$(echo "$line" | sed "s/shard-key=/shard-key={{.SHARD}}/")
        fi

        # Replace "name" to contain shard info
        if [[ $line == *"name: addon-controller"* ]]; then
            line=$(echo "$line" | sed "s/addon-controller/addon-controller-{{.SHARD}}/")
        fi

        # Write the line to the current section file
        echo "$line" >> "$current_section_file"
    fi
done < "$yaml_file"

# Iterate through the split sections and print those with "kind: Deployment"
for section_file in temp_yaml_sections/*.yaml; do
    if grep -q "kind: Deployment" "$section_file"; then
        cat "$section_file" > $output_file
    fi
done

# Remove the temporary directory
rm -r temp_yaml_sections