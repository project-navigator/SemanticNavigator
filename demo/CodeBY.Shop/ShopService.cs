using System.Globalization;
using System.Security.Cryptography;
using System.Text;

namespace CodeBY.Shop;

/// <summary>A small, dependency-free shop domain for code search demonstrations.</summary>
public class ShopService
{
    /// <summary>Reject expired access tokens and tokens absent from the allowlist.</summary>
    public bool ValidateToken(string token, DateTime expiresAt, ISet<string> allowedTokens)
    {
        return expiresAt > DateTime.UtcNow && allowedTokens.Contains(token);
    }

    /// <summary>Hash a password with a random salt using PBKDF2 SHA256.</summary>
    public string HashPassword(string password, byte[] salt)
    {
        var hash = Rfc2898DeriveBytes.Pbkdf2(password, salt, 100_000, HashAlgorithmName.SHA256, 32);
        return Convert.ToHexString(hash);
    }

    /// <summary>Calculate the invoice amount after a percentage discount.</summary>
    public decimal CalculateInvoiceTotal(IEnumerable<decimal> prices, decimal discountPercent)
    {
        if (discountPercent < 0 || discountPercent > 100)
            throw new ArgumentOutOfRangeException(nameof(discountPercent));
        return Math.Round(prices.Sum() * (1 - discountPercent / 100), 2);
    }

    /// <summary>Convert a ruble amount to another currency using the supplied rate.</summary>
    public decimal ConvertCurrency(decimal rubles, decimal exchangeRate)
    {
        if (exchangeRate <= 0) throw new ArgumentOutOfRangeException(nameof(exchangeRate));
        return Math.Round(rubles / exchangeRate, 2);
    }

    /// <summary>Reserve stock atomically in an in-memory inventory.</summary>
    public bool ReserveStock(IDictionary<string, int> inventory, string sku, int quantity)
    {
        lock (inventory)
        {
            if (quantity <= 0 || !inventory.TryGetValue(sku, out var available) || available < quantity)
                return false;
            inventory[sku] = available - quantity;
            return true;
        }
    }

    /// <summary>Return all orders created between two inclusive dates.</summary>
    public IEnumerable<Order> FindOrdersByDate(IEnumerable<Order> orders, DateTime from, DateTime until)
    {
        return orders.Where(order => order.CreatedAt >= from && order.CreatedAt <= until);
    }

    /// <summary>Split results into pages with a one-based page number.</summary>
    public IEnumerable<Order> PaginateOrders(IEnumerable<Order> orders, int page, int pageSize)
    {
        if (page < 1 || pageSize < 1) throw new ArgumentOutOfRangeException(nameof(page));
        return orders.Skip((page - 1) * pageSize).Take(pageSize);
    }

    /// <summary>Escape CSV cells containing quotes, commas or newlines.</summary>
    public string ExportOrdersCsv(IEnumerable<Order> orders)
    {
        static string Escape(string value) => "\"" + value.Replace("\"", "\"\"") + "\"";
        var rows = orders.Select(order => $"{Escape(order.Id)},{order.Total.ToString(CultureInfo.InvariantCulture)}");
        return "id,total\n" + string.Join("\n", rows);
    }

    /// <summary>Deduplicate customer addresses ignoring case and surrounding whitespace.</summary>
    public IEnumerable<string> DeduplicateEmails(IEnumerable<string> emails)
    {
        return emails.Select(email => email.Trim()).Distinct(StringComparer.OrdinalIgnoreCase);
    }

    /// <summary>Retry a transient operation up to a bounded number of attempts.</summary>
    public async Task<T> RetryAsync<T>(Func<Task<T>> operation, int maxAttempts, CancellationToken cancellationToken)
    {
        if (maxAttempts < 1) throw new ArgumentOutOfRangeException(nameof(maxAttempts));
        for (var attempt = 1; ; attempt++)
        {
            cancellationToken.ThrowIfCancellationRequested();
            try { return await operation(); }
            catch (HttpRequestException) when (attempt < maxAttempts)
            {
                await Task.Delay(TimeSpan.FromMilliseconds(100 * attempt), cancellationToken);
            }
        }
    }

    /// <summary>Reject uploads with unsupported extensions or excessive size.</summary>
    public bool ValidateUpload(string fileName, long bytes)
    {
        var extension = Path.GetExtension(fileName);
        return bytes > 0 && bytes <= 5_000_000 && new[] { ".png", ".jpg", ".pdf" }.Contains(extension.ToLowerInvariant());
    }

    /// <summary>Hide all but the last four digits of a bank card number.</summary>
    public string MaskCardNumber(string cardNumber)
    {
        var digits = new string(cardNumber.Where(char.IsDigit).ToArray());
        return digits.Length <= 4 ? digits : new string('*', digits.Length - 4) + digits[^4..];
    }
}

public record Order(string Id, decimal Total, DateTime CreatedAt);
